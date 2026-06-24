package handler

import (
	"errors"

	"github.com/gin-gonic/gin"

	"github.com/qs-lzh/flash-sale/pkg/logger"
	"github.com/qs-lzh/flash-sale/pkg/metrics"
	"github.com/qs-lzh/flash-sale/pkg/resilience"
	"github.com/qs-lzh/flash-sale/services/reservation/internal/cache"
	"github.com/qs-lzh/flash-sale/services/reservation/internal/model"
	"github.com/qs-lzh/flash-sale/services/reservation/internal/mq"
	"github.com/qs-lzh/flash-sale/services/reservation/internal/outbox"
	"github.com/qs-lzh/flash-sale/services/reservation/internal/repository"
)

type Service struct {
	Cache       *cache.RedisCache
	OutboxStore *outbox.Store
	Repo        *repository.Repo
}

func New(c *cache.RedisCache, store *outbox.Store, repo *repository.Repo) *Service {
	return &Service{Cache: c, OutboxStore: store, Repo: repo}
}

// HandleReserve godoc
//
//	@Summary		Reserve a ticket
//	@Description	Reserve a ticket for a showtime. Requires Bearer token.
//	@Tags			reservation
//	@Accept			json
//	@Produce		json
//	@Param			request	body		ReserveRequest	true	"Reservation request"
//	@Success		200		{object}	ReserveResponse
//	@Failure		400		{object}	ErrorResponse
//	@Failure		401		{object}	ErrorResponse
//	@Failure		409		{object}	ErrorResponse
//	@Failure		429		{object}	ErrorResponse
//	@Failure		503		{object}	ErrorResponse
//	@Security		BearerAuth
//	@Router			/reserve [post]
func (s *Service) HandleReserve(ctx *gin.Context) {
	metrics.ReservationTotal.Inc()

	var req ReserveRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		metrics.ReservationFailed.Inc()
		ctx.JSON(400, gin.H{"error": "Invalid request format", "detail": err.Error()})
		return
	}

	done, cbErr := resilience.RedisCB.Allow()
	if cbErr != nil {
		logger.Log.Errorf("Circuit breaker open (redis): %v", cbErr)
		metrics.ReservationFailed.Inc()
		ctx.JSON(503, gin.H{"error": "Service temporarily unavailable", "message": "Too many failures, please try again later"})
		return
	}

	reservationID, err := s.Cache.ReserveTicket(req.ShowtimeID, req.UserID)
	if err != nil {
		done(err)
		metrics.ReservationFailed.Inc()
		if errors.Is(err, cache.ErrSoldOut) {
			ctx.JSON(409, gin.H{"error": "Tickets sold out", "message": "Sorry, all tickets for this showtime have been sold out"})
			return
		}
		if errors.Is(err, cache.ErrAlreadyOrdered) {
			ctx.JSON(409, gin.H{"error": "Already ordered", "message": "You have already reserved a ticket for this showtime"})
			return
		}
		ctx.JSON(500, gin.H{"error": "Internal server error", "message": "Failed to process reservation, please try again later"})
		return
	}
	done(nil)

	// Persist saga state for tracking and compensation
	if err := s.Repo.CreateReservation(reservationID, req.ShowtimeID, req.UserID, model.SagaStateReserved); err != nil {
		logger.Log.Errorf("Failed to persist saga state: %v", err)
	}

	// Outbox pattern: write to outbox instead of directly publishing to MQ.
	if err := s.OutboxStore.Insert(mq.ReservationToPaymentImmediateQueue,
		mq.ReservationToPaymentImmediateMessage{
			ReservationID: reservationID,
			ShowtimeID:    req.ShowtimeID,
			UserID:        req.UserID,
			Price:         1,
		}); err != nil {
		logger.Log.Errorf("Failed to insert outbox record: %v", err)
		metrics.ReservationFailed.Inc()
		ctx.JSON(500, gin.H{"error": "Internal server error", "message": "Failed to process reservation, please try again later"})
		return
	}

	if err := s.OutboxStore.Insert(mq.ReservationToPaymentDelayQueue,
		mq.ReservationToPaymentDelayMessage{ReservationID: reservationID}); err != nil {
		logger.Log.Errorf("Failed to insert outbox record: %v", err)
		metrics.ReservationFailed.Inc()
		ctx.JSON(500, gin.H{"error": "Internal server error", "message": "Failed to process reservation, please try again later"})
		return
	}

	metrics.ReservationSuccess.Inc()
	ctx.JSON(200, ReserveResponse{
		Message:       "Ticket reserved successfully",
		ReservationID: reservationID,
		Status:        "RESERVED",
		Note:          "Please complete payment within 15 minutes",
	})
}

type ReserveRequest struct {
	UserID     uint `json:"user_id" example:"1"`
	ShowtimeID uint `json:"showtime_id" example:"1"`
}

type ReserveResponse struct {
	Message       string `json:"message"`
	ReservationID uint   `json:"reservation_id"`
	Status        string `json:"status"`
	Note          string `json:"note"`
}

type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message,omitempty"`
	Detail  string `json:"detail,omitempty"`
}
