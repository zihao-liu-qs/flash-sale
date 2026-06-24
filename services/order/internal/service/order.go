package service

import (
	"context"
	"encoding/json"

	"github.com/qs-lzh/flash-sale/pkg/logger"
	"github.com/qs-lzh/flash-sale/pkg/metrics"
	"github.com/qs-lzh/flash-sale/pkg/resilience"
	"github.com/qs-lzh/flash-sale/pkg/tracing"

	amqp "github.com/rabbitmq/amqp091-go"
	"gorm.io/gorm"

	"github.com/qs-lzh/flash-sale/services/order/internal/mq"
	"github.com/qs-lzh/flash-sale/services/order/internal/repository"
)

type Service struct {
	DB   *gorm.DB
	Repo *repository.Repo
}

func New(db *gorm.DB) *Service {
	return &Service{
		DB:   db,
		Repo: repository.New(db),
	}
}

func (s *Service) Start(mqConn *amqp.Connection) error {
	ch, err := mq.NewChannel(mqConn)
	if err != nil {
		return err
	}

	msgs, err := ch.Consume(mq.PaymentToOrderImmediateQueue, "", false, false, false, false, nil)
	if err != nil {
		return err
	}

	go func() {
		for msg := range msgs {
			if err := s.handleOrderCreation(msg); err != nil {
				logger.Log.Errorf("Failed to handle order creation: %v", err)
			}
		}
	}()

	return nil
}

func (s *Service) handleOrderCreation(msg amqp.Delivery) error {
	ctx := tracing.ExtractAMQPHeaders(context.Background(), msg.Headers)

	var message mq.PaymentToOrderImmediateMessage
	if err := json.Unmarshal(msg.Body, &message); err != nil {
		metrics.MQConsumedTotal.WithLabelValues(mq.PaymentToOrderImmediateQueue, "error").Inc()
		msg.Nack(false, false)
		return err
	}

	// All data comes from the message — no Redis read needed.
	// ShowtimeID and UserID were carried through the MQ chain:
	// Reservation → Payment → Order
	if err := s.createOrderFromReservation(ctx, message.ReservationID, message.ShowtimeID, message.UserID); err != nil {
		metrics.MQConsumedTotal.WithLabelValues(mq.PaymentToOrderImmediateQueue, "error").Inc()
		mq.NackWithRetry(msg)
		return err
	}

	metrics.MQConsumedTotal.WithLabelValues(mq.PaymentToOrderImmediateQueue, "success").Inc()
	metrics.OrderCreatedTotal.Inc()
	msg.Ack(false)
	return nil
}

func (s *Service) createOrderFromReservation(ctx context.Context, reservationID, showtimeID, userID uint) error {
	done, cbErr := resilience.PostgresCB.Allow()
	if cbErr != nil {
		return cbErr
	}

	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		_, err := s.Repo.WithTx(tx).GetByID(reservationID)
		if err == nil {
			return nil // idempotent: already created
		}
		return s.Repo.WithTx(tx).Create(reservationID, showtimeID, userID)
	})
	done(err)
	return err
}
