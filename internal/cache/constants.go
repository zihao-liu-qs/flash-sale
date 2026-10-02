package cache

import (
	"errors"
	"fmt"

	redis "github.com/redis/go-redis/v9"
)

// key names definition
// key names in lua script should follow these formats
const (
	ReservationKey      = "reservation:%d"     // key of reservation details, '%d' is reservation id
	ReservationIDSeqKey = "reservation:id:seq" // reservation id, a constant

	ShowtimeRemainingTicketsKey = "showtime:%d:ticket:remain" // key of remaining tickets of a showtime, '%d' is showtime id

	UserShowtimeOrderedKey = "user:%d:showtime:%d:ordered" // key of a user's reservation to a showtime, first '%d' is user id, second '%d' is showtime id
)

func MakeReservationKey(reservationID uint) string {
	return fmt.Sprintf("reservation:%d", reservationID)
}

func MakeShowtimeRemainingTicketsKey(showtimeID uint) string {
	return fmt.Sprintf("showtime:%d:ticket:remain", showtimeID)
}

func MakeUserShowtimeOrderedKey(userID uint, showtimeID uint) string {
	return fmt.Sprintf("user:%d:showtime:%d:ordered", userID, showtimeID)
}

// struct definitions
// the data put into redis in lua script should follow the struct
type ReservationCacheValue struct {
	ShowtimeID uint              `redis:"showtime_id"`
	SeatID     uint              `redis:"seat_id"`
	UserID     uint              `redis:"user_id"`
	Status     ReservationStatus `redis:"status"`
	CreatedAt  int64             `redis:"created_at"` // unix seconds, Redis server clock
}

type ReservationStatus string

var (
	ReservationStatusReserved ReservationStatus = "RESERVED"
	ReservationStatusPaid     ReservationStatus = "PAID"
	ReservationStatusTimeout  ReservationStatus = "TIMEOUT"
)

// errors
var (
	ErrSoldOut        = errors.New("Tickets sold out")
	ErrAlreadyOrdered = errors.New("User already ordered this showtime")
)

// lua scripts
var initTicketsScript = redis.NewScript(`
-- ARGV: key1 value1 key2 value2 ...
for i = 1, #ARGV, 2 do
    local key = ARGV[i]
    local value = tonumber(ARGV[i + 1])
    redis.call("SET", key, value)
end
return #ARGV / 2
`)

var reserveTicketScript = redis.NewScript(`
	-- KEYS[1] = showtime:{showtime_id}:ticket:remain
	-- KEYS[2] = reservation:id:seq
	-- KEYS[3] = user:{user_id}:showtime:{showtime_id}:ordered

	-- ARGV[1] = showtime_id
	-- ARGV[2] = user_id

	-- 检查用户是否已经订过该场次的票
	local userOrderedKey = KEYS[3]
	local hasOrdered = redis.call("GET", userOrderedKey)
	if hasOrdered then
		return -3  -- 表示用户已订单
	end

	-- 检查剩余票数
	local remain = tonumber(redis.call("GET", KEYS[1]))
	if (not remain) or remain <= 0 then
		return -1  -- 表示售罄
	end

	-- 扣库存
	redis.call("DECR", KEYS[1])

	-- 生成 reservation_id
	local id = redis.call("INCR", KEYS[2])

	local resKey = "reservation:" .. id

	-- 记录创建时间（取 Redis 服务器时钟，避免各应用机器时钟不一致），
	-- 对账任务靠它识别滞留的 RESERVED 订单
	local t = redis.call("TIME")

	-- 创建 reservation
	redis.call("HSET", resKey,
		"showtime_id", ARGV[1],
		"user_id", ARGV[2],
		"status", "RESERVED",
		"created_at", t[1]
	)

	-- 标记用户已订单 (无过期时间，永久有效)
	redis.call("SET", userOrderedKey, "true")

	return id
`)

var markTicketAsPaidScript = redis.NewScript(`
	-- KEYS[1] = reservation:{reservation_id}

	-- 幂等设计（at-least-once 投递下消费者必须幂等）：
	--   1  : RESERVED -> PAID，新支付成功
	--   0  : 已是 PAID，幂等命中——上次支付成功但死在发入库消息，调用方应补发
	--  -3  : 订单不存在（已被回滚），残留消息，调用方应直接忽略
	--  -2  : TIMEOUT 等其他状态，真失败
	local resKey = KEYS[1]
	local status = redis.call("HGET", resKey, "status")
	if not status then
		return -3
	end
	if status == "PAID" then
		return 0
	end
	if status ~= "RESERVED" then
		return -2
	end

	redis.call("HSET", resKey, "status", "PAID")
	return 1
`)

var markTicketAsTimeoutScript = redis.NewScript(`
	-- KEYS[1] = reservation:{reservation_id}

	local resKey = KEYS[1]
	local status = redis.call("HGET", resKey, "status")
	local showtime_id = redis.call("HGET", resKey, "showtime_id")

	-- reservation 不存在：说明该订单已被回滚（消息发送失败时），
	-- 延时消息属于残留消息，消费端应视为已处理而非失败重试
	if not status then
		return -3
	end

	if status ~= "RESERVED" then
		return -2
	end

	-- 构建库存键
	local remainKey = "showtime:" .. showtime_id .. ":ticket:remain"

	-- 更新状态为超时
	redis.call("HSET", resKey, "status", "TIMEOUT")

	-- 增加对应场次的剩余票数
	redis.call("INCR", remainKey)

	return 1
`)

var cancelReservationScript = redis.NewScript(`
	-- KEYS[1] = reservation:{reservation_id}
	-- KEYS[2] = showtime:{showtime_id}:ticket:remain
	-- KEYS[3] = user:{user_id}:showtime:{showtime_id}:ordered

	-- 回滚一次预订：删除 reservation、回滚库存、释放用户购买资格。
	-- 用于 MQ 消息发送失败时撤销已创建的订单，让用户可以安全重试。
	-- 幂等：reservation 不存在时直接返回，避免重复回滚导致库存多加。
	local exists = redis.call("EXISTS", KEYS[1])
	if exists == 0 then
		return -1
	end

	redis.call("DEL", KEYS[1])
	redis.call("INCR", KEYS[2])
	redis.call("DEL", KEYS[3])

	return 1
`)
