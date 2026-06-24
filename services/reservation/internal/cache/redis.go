package cache

import (
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

func (r *RedisCache) Init(showtimeIDTicketsMap map[uint]int) error {
	if err := r.Client.FlushDB(pkgcache.Ctx).Err(); err != nil {
		return err
	}
	args := make([]any, 0, len(showtimeIDTicketsMap)*2)
	for showtimeID, tickets := range showtimeIDTicketsMap {
		key := MakeShowtimeRemainingTicketsKey(showtimeID)
		args = append(args, key, tickets)
	}
	_, err := initTicketsScript.Run(pkgcache.Ctx, r.Client, []string{}, args...).Result()
	return err
}

func (r *RedisCache) ReserveTicket(showtimeID, userID uint) (uint, error) {
	remainingKey := MakeShowtimeRemainingTicketsKey(showtimeID)
	userOrderedKey := MakeUserShowtimeOrderedKey(userID, showtimeID)
	res, err := reserveTicketScript.Run(pkgcache.Ctx, r.Client,
		[]string{remainingKey, ReservationIDSeqKey, userOrderedKey},
		showtimeID, userID).Int64()
	if err != nil {
		return 0, err
	}
	if res == -1 {
		return 0, ErrSoldOut
	}
	if res == -3 {
		return 0, ErrAlreadyOrdered
	}
	return uint(res), nil
}

func (r *RedisCache) MarkTicketAsPaid(reservationID uint) error {
	_, err := markTicketAsPaidScript.Run(pkgcache.Ctx, r.Client,
		[]string{MakeReservationKey(reservationID)}).Result()
	return err
}

func (r *RedisCache) MarkTicketAsTimeout(reservationID uint) error {
	_, err := markTicketAsTimeoutScript.Run(pkgcache.Ctx, r.Client,
		[]string{MakeReservationKey(reservationID)}).Result()
	return err
}

func (r *RedisCache) GetReservationInfo(reservationID uint) (map[string]string, error) {
	return r.Client.HGetAll(pkgcache.Ctx, MakeReservationKey(reservationID)).Result()
}
