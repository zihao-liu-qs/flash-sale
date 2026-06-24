package mq

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/qs-lzh/flash-sale/pkg/tracing"
)

// Queue names — the contract between services
const (
	ReservationToPaymentImmediateQueue = "reservation.payment.pay.immediate"

	ReservationToPaymentDelayQueue     = "reservation.payment.timeout.delay"
	ReservationToPaymentTimeoutQueue   = "reservation.payment.timeout.immediate"
	ReservationToPaymentTimeoutRouting = "reservation.timeout"

	PaymentToOrderImmediateQueue = "payment.order.create.immediate"
	SagaStateQueue             = "reservation.saga.state"
)

// Message types
type ReservationToPaymentImmediate struct {
	ReservationID uint `json:"reservation_id"`
	ShowtimeID    uint `json:"showtime_id"`
	UserID        uint `json:"user_id"`
	Price         int  `json:"price"`
}

type ReservationToPaymentDelay struct {
	ReservationID uint `json:"reservation_id"`
}

type PaymentToOrderImmediate struct {
	ReservationID uint `json:"reservation_id"`
	ShowtimeID    uint `json:"showtime_id"`
	UserID        uint `json:"user_id"`
}

type SagaStateMessage struct {
	ReservationID uint   `json:"reservation_id"`
	State         string `json:"state"`
}

func NewConn(url string) (*amqp.Connection, error) {
	return amqp.Dial(url)
}

func NewChannel(conn *amqp.Connection) (*amqp.Channel, error) {
	return conn.Channel()
}

func Publish(ctx context.Context, ch *amqp.Channel, queueName string, message any) error {
	body, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}
	headers := amqp.Table{}
	tracing.InjectAMQPHeaders(ctx, headers)
	return ch.PublishWithContext(ctx, "", queueName, false, false,
		amqp.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent,
			Body:         body,
			Timestamp:    time.Now(),
			Headers:      headers,
		})
}

func InitQueues(conn *amqp.Connection) error {
	ch, err := NewChannel(conn)
	if err != nil {
		return err
	}
	defer ch.Close()

	if err := setupImmediateQueue(ch, ReservationToPaymentImmediateQueue); err != nil {
		return err
	}
	exchangeName := "reservation.timeout.exchange"
	if err := setupDelayQueue(ch, ReservationToPaymentDelayQueue, exchangeName,
		ReservationToPaymentTimeoutQueue, ReservationToPaymentTimeoutRouting); err != nil {
		return err
	}
	if err := setupImmediateQueue(ch, PaymentToOrderImmediateQueue); err != nil {
		return err
	}
	if err := setupImmediateQueue(ch, SagaStateQueue); err != nil {
		return err
	}

	ClearQueue(conn, ReservationToPaymentImmediateQueue)
	ClearQueue(conn, ReservationToPaymentDelayQueue)
	ClearQueue(conn, ReservationToPaymentTimeoutQueue)
	ClearQueue(conn, PaymentToOrderImmediateQueue)

	return nil
}

func setupImmediateQueue(ch *amqp.Channel, name string) error {
	_, err := ch.QueueDeclare(name, true, false, false, false, nil)
	return err
}

func setupDelayQueue(ch *amqp.Channel, delayQ, exchangeName, timeoutQ, routingKey string) error {
	delayArgs := amqp.Table{
		"x-message-ttl":             int32(15 * 60 * 1000),
		"x-dead-letter-exchange":    exchangeName,
		"x-dead-letter-routing-key": routingKey,
	}
	if _, err := ch.QueueDeclare(delayQ, true, false, false, false, delayArgs); err != nil {
		return err
	}
	if err := ch.ExchangeDeclare(exchangeName, "direct", true, false, false, false, nil); err != nil {
		return err
	}
	if _, err := ch.QueueDeclare(timeoutQ, true, false, false, false, nil); err != nil {
		return err
	}
	return ch.QueueBind(timeoutQ, routingKey, exchangeName, false, nil)
}

func ClearQueue(conn *amqp.Connection, name string) error {
	ch, err := NewChannel(conn)
	if err != nil {
		return err
	}
	defer ch.Close()
	_, err = ch.QueuePurge(name, false)
	return err
}

const maxRetries = 3

// NackWithRetry rejects a message with requeue if retries remain.
// After maxRetries, the message is nack'd without requeue (sent to DLX or dropped).
func NackWithRetry(msg amqp.Delivery) {
	count := getRetryCount(msg.Headers)
	if _, ok := msg.Headers["x-retry-count"]; !ok {
		if msg.Headers == nil {
			msg.Headers = amqp.Table{}
		}
		msg.Headers["x-retry-count"] = count + 1
	}
	if count < maxRetries {
		msg.Nack(false, true)
	} else {
		msg.Nack(false, false)
	}
}

func getRetryCount(headers amqp.Table) int32 {
	if headers == nil {
		return 0
	}
	v, ok := headers["x-retry-count"]
	if !ok {
		return 0
	}
	switch c := v.(type) {
	case int32:
		return c
	case int64:
		return int32(c)
	case int:
		return int32(c)
	default:
		return 0
	}
}
