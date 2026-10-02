package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	redis "github.com/redis/go-redis/v9"
)

var ctx = context.Background()

type RedisCache struct {
	Client *redis.Client
}

func NewRedisCache(url string) (*RedisCache, error) {
	client := redis.NewClient(
		&redis.Options{
			Addr:     url,
			Password: "",
			DB:       0,
		},
	)
	redisCache := &RedisCache{Client: client}

	return redisCache, nil
}

func (r *RedisCache) Init(showtimeIDTicketsMap map[uint]int) error {
	if err := r.Client.FlushDB(context.Background()).Err(); err != nil {
		return err
	}
	if err := r.initRemainingTickets(showtimeIDTicketsMap); err != nil {
		return err
	}
	return nil
}

func (r *RedisCache) initRemainingTickets(showtimeIDTicketsMap map[uint]int) error {
	args := make([]any, 0, len(showtimeIDTicketsMap)*2)
	for showtimeID, tickets := range showtimeIDTicketsMap {
		key := MakeShowtimeRemainingTicketsKey(showtimeID) // "showtime:{id}:ticket:remain"
		args = append(args, key, tickets)
	}

	_, err := initTicketsScript.Run(ctx, r.Client, []string{}, args...).Result()
	if err != nil {
		return err
	}

	return nil
}

func (r *RedisCache) Set(key string, value any, expiration time.Duration) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return r.Client.Set(ctx, key, data, expiration).Err()
}

func (r *RedisCache) Get(key string, dest any) error {
	data, err := r.Client.Get(ctx, key).Bytes()
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dest)
}

func (r *RedisCache) SetBool(key string, value bool) error {
	strValue := "false"
	if value {
		strValue = "true"
	}
	return r.Client.Set(ctx, key, strValue, 5*time.Minute).Err()
}

func (r *RedisCache) GetBool(key string) (value bool, err error) {
	value, err = r.Client.Get(ctx, key).Bool()
	if err != nil {
		return false, err
	}
	return value, nil
}

/*
* remaining tickets of a showtime
 */

// create a reservation in redis if there's tickets available
func (r *RedisCache) ReserveTicket(showtimeID uint, userID uint) (reservationID uint, err error) {
	remainingTicketsKey := MakeShowtimeRemainingTicketsKey(showtimeID)
	userShowtimeOrderedKey := MakeUserShowtimeOrderedKey(userID, showtimeID)
	res, err := reserveTicketScript.Run(ctx, r.Client, []string{remainingTicketsKey, ReservationIDSeqKey, userShowtimeOrderedKey}, showtimeID, userID).Int64()
	if err != nil {
		return 0, err
	}
	if res == -1 {
		return 0, ErrSoldOut
	}
	if res == -3 {
		return 0, ErrAlreadyOrdered
	}

	reservationID = uint(res)

	return reservationID, nil
}

// PaidStatus is the outcome of MarkTicketAsPaid, letting the caller tell a
// fresh payment apart from an idempotent hit and a rolled-back reservation.
type PaidStatus int

const (
	PaidNew      PaidStatus = iota // RESERVED -> PAID transition just happened
	PaidAlready                    // already PAID: idempotent success, resend follow-up message
	PaidNotFound                   // reservation missing (rolled back): drop the message
)

// MarkTicketAsPaid transitions a reservation to PAID. It is idempotent so
// that retried payment messages are safe: an already-PAID reservation reports
// PaidAlready instead of an error, and a missing (rolled-back) reservation
// reports PaidNotFound.
func (r *RedisCache) MarkTicketAsPaid(reservationID uint) (PaidStatus, error) {
	res, err := markTicketAsPaidScript.Run(ctx, r.Client, []string{fmt.Sprintf("reservation:%d", reservationID)}).Int64()
	if err != nil {
		return 0, err
	}
	switch res {
	case 1:
		return PaidNew, nil
	case 0:
		return PaidAlready, nil
	case -3:
		return PaidNotFound, nil
	default: // -2
		return 0, errors.New("invalid reservation status")
	}
}

// mark ticket as timeout and roll back remaining tickets in redis
func (r *RedisCache) MarkTicketAsTimeout(reservationID uint) error {
	res, err := markTicketAsTimeoutScript.Run(ctx, r.Client, []string{fmt.Sprintf("reservation:%d", reservationID)}).Result()
	if err != nil {
		return err
	}
	if res == int64(-3) {
		// reservation doesn't exist (already rolled back after an MQ send
		// failure): the stale delay message is considered handled
		return nil
	}
	if res == int64(-2) {
		return errors.New("invalid reservation status")
	}
	return nil
}

// CancelReservation rolls back a reservation when the MQ messages fail to
// send: deletes the reservation, returns the ticket to the pool and releases
// the user's purchase eligibility, so the user can safely retry.
// It is idempotent: cancelling a non-existent reservation is a no-op.
func (r *RedisCache) CancelReservation(reservationID uint, showtimeID uint, userID uint) error {
	reservationKey := MakeReservationKey(reservationID)
	remainingTicketsKey := MakeShowtimeRemainingTicketsKey(showtimeID)
	userShowtimeOrderedKey := MakeUserShowtimeOrderedKey(userID, showtimeID)
	_, err := cancelReservationScript.Run(ctx, r.Client,
		[]string{reservationKey, remainingTicketsKey, userShowtimeOrderedKey}).Result()
	return err
}

func (r *RedisCache) ReleaseTicket(showtimeID uint) error {
	key := MakeShowtimeRemainingTicketsKey(showtimeID)
	return r.Client.Incr(ctx, key).Err()
}

/*
* user ordered showtime
 */
func (r *RedisCache) SetOrdered(userID uint, showtimeID uint) error {
	key := MakeUserShowtimeOrderedKey(userID, showtimeID)
	return r.Client.Set(ctx, key, true, 0).Err()
}

func (r *RedisCache) GetOrdered(userID uint, showtimeID uint) (bool, error) {
	key := MakeUserShowtimeOrderedKey(userID, showtimeID)
	exist, err := r.Client.Get(ctx, key).Bool()
	if err != nil {
		// if the user doesn't order the showtime
		if errors.Is(err, redis.Nil) {
			return false, nil
		}
		// internal error
		return false, err
	}
	// if the user doesn't order the showtime
	return exist, nil
}

func (r *RedisCache) GetReservationInfo(reservationID uint) (map[string]string, error) {
	key := MakeReservationKey(reservationID)
	return r.Client.HGetAll(ctx, key).Result()
}

// ReservationInfo is the projection of a reservation hash used by the
// reconcile workflow.
type ReservationInfo struct {
	ID         uint
	ShowtimeID uint
	UserID     uint
	Status     ReservationStatus
	CreatedAt  int64 // unix seconds, Redis server clock
}

// ScanReservations iterates every reservation hash via SCAN — never KEYS,
// which would block Redis's single thread — and invokes fn for each.
// HGETALL calls are pipelined per SCAN batch to keep round-trips low.
// An error from fn aborts the scan; transient per-reservation failures
// should be logged inside fn, which should then return nil.
func (r *RedisCache) ScanReservations(fn func(ReservationInfo) error) error {
	var cursor uint64
	for {
		keys, next, err := r.Client.Scan(ctx, cursor, "reservation:*", 100).Result()
		if err != nil {
			return err
		}

		pipe := r.Client.Pipeline()
		cmds := make(map[string]*redis.MapStringStringCmd, len(keys))
		for _, key := range keys {
			if key == ReservationIDSeqKey {
				continue // the id sequence matches the pattern but is not a hash
			}
			cmds[key] = pipe.HGetAll(ctx, key)
		}
		if len(cmds) > 0 {
			if _, err := pipe.Exec(ctx); err != nil {
				return err
			}
		}

		for key, cmd := range cmds {
			fields, err := cmd.Result()
			if err != nil {
				return fmt.Errorf("failed to read %s: %w", key, err)
			}
			if len(fields) == 0 {
				continue // deleted between SCAN and HGETALL (e.g. rolled back)
			}
			info, err := parseReservationInfo(key, fields)
			if err != nil {
				return err
			}
			if err := fn(info); err != nil {
				return err
			}
		}

		cursor = next
		if cursor == 0 {
			return nil
		}
	}
}

func parseReservationInfo(key string, fields map[string]string) (ReservationInfo, error) {
	var info ReservationInfo
	id, err := strconv.ParseUint(strings.TrimPrefix(key, "reservation:"), 10, 64)
	if err != nil {
		return info, fmt.Errorf("invalid reservation key %q: %w", key, err)
	}
	info.ID = uint(id)
	if v, ok := fields["showtime_id"]; ok {
		n, _ := strconv.ParseUint(v, 10, 64)
		info.ShowtimeID = uint(n)
	}
	if v, ok := fields["user_id"]; ok {
		n, _ := strconv.ParseUint(v, 10, 64)
		info.UserID = uint(n)
	}
	info.Status = ReservationStatus(fields["status"])
	if v, ok := fields["created_at"]; ok {
		// created_at 缺失（对账功能上线前创建的旧订单）时保持零值：
		// 对 RESERVED 意味着 age 无穷大，会被立即超时清理——正是期望行为
		n, _ := strconv.ParseInt(v, 10, 64)
		info.CreatedAt = n
	}
	return info, nil
}
