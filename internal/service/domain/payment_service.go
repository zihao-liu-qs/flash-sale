package domain

import (
	"math/rand"
	"time"

	"github.com/qs-lzh/flash-sale/internal/cache"
)

type PaymentService interface {
	// StartMockPay simulates paying and marks the reservation PAID.
	// Idempotent: see cache.PaidStatus for the possible outcomes.
	StartMockPay(reservationID uint) (cache.PaidStatus, error)
	MarkTimeout(reservationID uint) error
}

type paymentService struct {
	Cache *cache.RedisCache
}

func NewPaymentService(cache *cache.RedisCache) *paymentService {
	return &paymentService{
		Cache: cache,
	}
}

var _ PaymentService = (*paymentService)(nil)

func (s *paymentService) StartMockPay(reservationID uint) (cache.PaidStatus, error) {
	time.Sleep(time.Duration(rand.Intn(901)+100) * time.Millisecond)

	return s.Cache.MarkTicketAsPaid(reservationID)
}

func (s *paymentService) MarkTimeout(reservationID uint) error {
	return s.Cache.MarkTicketAsTimeout(reservationID)
}
