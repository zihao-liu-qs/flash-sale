package cache

import (
	"fmt"

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

func (r *RedisCache) GetReservationInfo(reservationID uint) (map[string]string, error) {
	key := fmt.Sprintf("reservation:%d", reservationID)
	return r.Client.HGetAll(pkgcache.Ctx, key).Result()
}
