package mq

import (
	"context"

	amqp "github.com/rabbitmq/amqp091-go"

	pkgmq "github.com/qs-lzh/flash-sale/pkg/mq"
)

// Re-export queue names
const (
	ReservationToPaymentImmediateQueue = pkgmq.ReservationToPaymentImmediateQueue

	ReservationToPaymentDelayQueue      = pkgmq.ReservationToPaymentDelayQueue
	ReservationToPaymentTimeoutQueue    = pkgmq.ReservationToPaymentTimeoutQueue
	ReservationToPaymentTimeoutExchange = "reservation.timeout.exchange"
	ReservationToPaymentTimeoutRouting  = pkgmq.ReservationToPaymentTimeoutRouting

	PaymentToOrderImmediateQueue = pkgmq.PaymentToOrderImmediateQueue
	SagaStateQueue             = pkgmq.SagaStateQueue
)

// Re-export message types
type (
	ReservationToPaymentImmediateMessage = pkgmq.ReservationToPaymentImmediate
	ReservationToPaymentDelayMessage     = pkgmq.ReservationToPaymentDelay
	PaymentToOrderImmediateMessage       = pkgmq.PaymentToOrderImmediate
	SagaStateMessage                     = pkgmq.SagaStateMessage
)

var (
	NewMQConn     = pkgmq.NewConn
	NewChannel    = pkgmq.NewChannel
	InitQueues    = pkgmq.InitQueues
	NackWithRetry = pkgmq.NackWithRetry
)

func SendImmediateMessage(ctx context.Context, ch *amqp.Channel, queueName string, message any) error {
	return pkgmq.Publish(ctx, ch, queueName, message)
}
