package cache

import (
	"github.com/redis/go-redis/v9"

	pkgcache "github.com/qs-lzh/flash-sale/pkg/cache"
)

// Re-export key helpers from shared pkg
var (
	MakeReservationKey           = pkgcache.MakeReservationKey
	MakeShowtimeRemainingTicketsKey = pkgcache.MakeShowtimeRemainingTicketsKey
	MakeUserShowtimeOrderedKey     = pkgcache.MakeUserShowtimeOrderedKey
)

// Re-export key constants
const (
	ReservationKey             = pkgcache.ReservationKeyPattern
	ReservationIDSeqKey        = pkgcache.ReservationIDSeqKey
	ShowtimeRemainingTicketsKey = pkgcache.ShowtimeRemainingTicketsKey
	UserShowtimeOrderedKey      = pkgcache.UserShowtimeOrderedKey
)

// Re-export status and error types
type ReservationStatus = pkgcache.ReservationStatus

const (
	ReservationStatusReserved = pkgcache.ReservationStatusReserved
	ReservationStatusPaid     = pkgcache.ReservationStatusPaid
	ReservationStatusTimeout  = pkgcache.ReservationStatusTimeout
)

var (
	ErrSoldOut        = pkgcache.ErrSoldOut
	ErrAlreadyOrdered = pkgcache.ErrAlreadyOrdered
)

var initTicketsScript = redis.NewScript(`
for i = 1, #ARGV, 2 do
    local key = ARGV[i]
    local value = tonumber(ARGV[i + 1])
    redis.call("SET", key, value)
end
return #ARGV / 2
`)

var reserveTicketScript = redis.NewScript(`
	local ticketKey = KEYS[1]
	local seqKey = KEYS[2]
	local userOrderedKey = KEYS[3]

	local hasOrdered = redis.call("GET", userOrderedKey)
	if hasOrdered then
		return -3
	end

	local remain = tonumber(redis.call("GET", ticketKey))
	if (not remain) or remain <= 0 then
		return -1
	end

	redis.call("DECR", ticketKey)
	local id = redis.call("INCR", seqKey)
	local resKey = "reservation:" .. id

	redis.call("HSET", resKey,
		"showtime_id", ARGV[1],
		"user_id", ARGV[2],
		"status", "RESERVED"
	)

	redis.call("SET", userOrderedKey, "true")
	return id
`)

var markTicketAsPaidScript = redis.NewScript(`
	local resKey = KEYS[1]
	local status = redis.call("HGET", resKey, "status")
	if not status or status ~= "RESERVED" then
		return -2
	end
	redis.call("HSET", resKey, "status", "PAID")
	return 1
`)

var markTicketAsTimeoutScript = redis.NewScript(`
	local resKey = KEYS[1]
	local status = redis.call("HGET", resKey, "status")
	local showtime_id = redis.call("HGET", resKey, "showtime_id")
	if not status or status ~= "RESERVED" then
		return -2
	end
	local remainKey = "showtime:" .. showtime_id .. ":ticket:remain"
	redis.call("HSET", resKey, "status", "TIMEOUT")
	redis.call("INCR", remainKey)
	return 1
`)
