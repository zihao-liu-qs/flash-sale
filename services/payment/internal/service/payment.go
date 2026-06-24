package service

import (
	"context"
	"encoding/json"
	"math/rand"
	"time"

	"github.com/qs-lzh/flash-sale/pkg/logger"
	"github.com/qs-lzh/flash-sale/pkg/metrics"
	"github.com/qs-lzh/flash-sale/pkg/tracing"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/qs-lzh/flash-sale/services/payment/internal/mq"
)

type Service struct {
	MQConn *amqp.Connection
}

func New(mqConn *amqp.Connection) *Service {
	return &Service{MQConn: mqConn}
}

func (s *Service) Start() error {
	if err := s.consumePaymentCreate(); err != nil {
		return err
	}
	if err := s.consumePaymentTimeout(); err != nil {
		return err
	}
	return nil
}

func (s *Service) consumePaymentCreate() error {
	ch, err := mq.NewChannel(s.MQConn)
	if err != nil {
		return err
	}

	msgs, err := ch.Consume(mq.ReservationToPaymentImmediateQueue, "", false, false, false, false, nil)
	if err != nil {
		return err
	}

	go func() {
		for msg := range msgs {
			go func() {
				message, err := s.handlePayment(msg)
				if err != nil {
					logger.Log.Errorf("Failed to handle payment message: %v", err)
				} else {
					ctx := tracing.ExtractAMQPHeaders(context.Background(), msg.Headers)
					if err := mq.SendImmediateMessage(ctx, ch, mq.PaymentToOrderImmediateQueue,
						mq.PaymentToOrderImmediateMessage{
							ReservationID: message.ReservationID,
							ShowtimeID:    message.ShowtimeID,
							UserID:        message.UserID,
						}); err != nil {
						logger.Log.Errorf("Failed to send message: %v", err)
					}
					// Saga: notify reservation of payment success
					mq.SendImmediateMessage(ctx, ch, mq.SagaStateQueue,
						mq.SagaStateMessage{ReservationID: message.ReservationID, State: "PAID"})
				}
			}()
		}
	}()

	return nil
}

func (s *Service) handlePayment(msg amqp.Delivery) (*mq.ReservationToPaymentImmediateMessage, error) {
	var message mq.ReservationToPaymentImmediateMessage
	if err := json.Unmarshal(msg.Body, &message); err != nil {
		metrics.MQConsumedTotal.WithLabelValues(mq.ReservationToPaymentImmediateQueue, "error").Inc()
		msg.Nack(false, false)
		return nil, err
	}

	start := time.Now()
	// Payment only simulates processing — no Redis operation.
	// State change is handled by Reservation's Saga consumer.
	s.mockPay()

	metrics.PaymentDuration.Observe(time.Since(start).Seconds())
	metrics.PaymentTotal.WithLabelValues("success").Inc()
	metrics.MQConsumedTotal.WithLabelValues(mq.ReservationToPaymentImmediateQueue, "success").Inc()
	msg.Ack(false)
	return &message, nil
}

func (s *Service) mockPay() {
	time.Sleep(time.Duration(rand.Intn(901)+100) * time.Millisecond)
}

func (s *Service) consumePaymentTimeout() error {
	ch, err := mq.NewChannel(s.MQConn)
	if err != nil {
		return err
	}

	msgs, err := ch.Consume(mq.ReservationToPaymentTimeoutQueue, "", false, false, false, false, nil)
	if err != nil {
		return err
	}

	go func() {
		for msg := range msgs {
			s.handleTimeout(msg, ch)
		}
	}()

	return nil
}

func (s *Service) handleTimeout(msg amqp.Delivery, ch *amqp.Channel) {
	var message mq.ReservationToPaymentDelayMessage
	if err := json.Unmarshal(msg.Body, &message); err != nil {
		metrics.MQConsumedTotal.WithLabelValues(mq.ReservationToPaymentTimeoutQueue, "error").Inc()
		msg.Nack(false, false)
		return
	}

	// No direct Redis operation — only notify Reservation via Saga.
	// Reservation's Saga consumer will release the ticket.
	mq.SendImmediateMessage(context.Background(), ch, mq.SagaStateQueue,
		mq.SagaStateMessage{ReservationID: message.ReservationID, State: "TIMEOUT"})

	metrics.MQConsumedTotal.WithLabelValues(mq.ReservationToPaymentTimeoutQueue, "success").Inc()
	msg.Ack(false)
}
