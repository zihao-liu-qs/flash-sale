package mq

import (
	"context"

	amqp "github.com/rabbitmq/amqp091-go"

	pkgmq "github.com/qs-lzh/flash-sale/pkg/mq"
)

// Re-exported from shared pkg
var (
	NewMQConn     = pkgmq.NewConn
	NewChannel    = pkgmq.NewChannel
	NackWithRetry = pkgmq.NackWithRetry
)

const (
	ReservationToPaymentImmediateQueue = pkgmq.ReservationToPaymentImmediateQueue
	ReservationToPaymentDelayQueue     = pkgmq.ReservationToPaymentDelayQueue
	SagaStateQueue                    = pkgmq.SagaStateQueue
)

type (
	ReservationToPaymentImmediateMessage = pkgmq.ReservationToPaymentImmediate
	ReservationToPaymentDelayMessage     = pkgmq.ReservationToPaymentDelay
	SagaStateMessage                     = pkgmq.SagaStateMessage
)

func SendImmediateMessage(ctx context.Context, ch *amqp.Channel, queueName string, message any) error {
	return pkgmq.Publish(ctx, ch, queueName, message)
}

func SendTimeoutMessage(ctx context.Context, ch *amqp.Channel, delayQueueName string, message any) error {
	return pkgmq.Publish(ctx, ch, delayQueueName, message)
}
