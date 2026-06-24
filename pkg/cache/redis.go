package cache

import (
	"context"
	"errors"
	"fmt"

	"github.com/redis/go-redis/v9"
)

var Ctx = context.Background()

type RedisCache struct {
	Client *redis.Client
}

func NewRedisCache(url string) (*RedisCache, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     url,
		Password: "",
		DB:       0,
	})
	return &RedisCache{Client: client}, nil
}

// Key patterns
const (
	ReservationKeyPattern      = "reservation:%d"
	ReservationIDSeqKey        = "reservation:id:seq"
	ShowtimeRemainingTicketsKey = "showtime:%d:ticket:remain"
	UserShowtimeOrderedKey      = "user:%d:showtime:%d:ordered"
)

type ReservationStatus string

const (
	ReservationStatusReserved ReservationStatus = "RESERVED"
	ReservationStatusPaid     ReservationStatus = "PAID"
	ReservationStatusTimeout  ReservationStatus = "TIMEOUT"
)

var (
	ErrSoldOut        = errors.New("Tickets sold out")
	ErrAlreadyOrdered = errors.New("User already ordered this showtime")
)

func MakeReservationKey(reservationID uint) string {
	return fmt.Sprintf(ReservationKeyPattern, reservationID)
}

func MakeShowtimeRemainingTicketsKey(showtimeID uint) string {
	return fmt.Sprintf(ShowtimeRemainingTicketsKey, showtimeID)
}

func MakeUserShowtimeOrderedKey(userID, showtimeID uint) string {
	return fmt.Sprintf(UserShowtimeOrderedKey, userID, showtimeID)
}
