package cache

import (
	"fmt"

	"github.com/redis/go-redis/v9"

	pkgcache "github.com/qs-lzh/flash-sale/pkg/cache"
)

type RedisCache struct {
	*pkgcache.RedisCache
}

func NewRedisCache(url string) (*RedisCache, error) {
	c, err := pkgcache.NewRedisCache(url)
	if err != nil {
		return nil, err
	}
	return &RedisCache{RedisCache: c}, nil
}

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

func (r *RedisCache) MarkTicketAsPaid(reservationID uint) error {
	_, err := markTicketAsPaidScript.Run(pkgcache.Ctx, r.Client,
		[]string{fmt.Sprintf("reservation:%d", reservationID)}).Result()
	return err
}

func (r *RedisCache) MarkTicketAsTimeout(reservationID uint) error {
	_, err := markTicketAsTimeoutScript.Run(pkgcache.Ctx, r.Client,
		[]string{fmt.Sprintf("reservation:%d", reservationID)}).Result()
	return err
}
