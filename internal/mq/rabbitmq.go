package mq

import (
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"
)

func InitQueues(mqConn *amqp.Connection) error {
	ch, err := NewChannel(mqConn)
	if err != nil {
		return err
	}
	defer ch.Close()

	// demo mode: drop leftover queues from previous runs so tests always
	// start from a clean slate (this also lets queue arguments evolve across
	// versions — RabbitMQ rejects redeclaring a queue with different args).
	// In production the broker should keep the backlog instead of deleting it.
	queues := []string{
		ReservationToPaymentImmediateQueue,
		ReservationToPaymentRetryQueue,
		ReservationToPaymentParkingQueue,
		ReservationToPaymentDelayQueue,
		ReservationToPaymentTimeoutQueue,
		ReservationToPaymentTimeoutRetryQueue,
		ReservationToPaymentTimeoutParkingQueue,
		PaymentToOrderImmediateQueue,
		PaymentToOrderRetryQueue,
		PaymentToOrderParkingQueue,
	}
	for _, q := range queues {
		_, _ = ch.QueueDelete(q, false, false, false) // ignore "not found" errors
	}

	// payment queue, with retry infrastructure
	if err := SetupRetryableQueue(ch, ReservationToPaymentImmediateQueue,
		ReservationToPaymentRetryQueue, ReservationToPaymentParkingQueue); err != nil {
		return err
	}

	// timeout queue, with retry infrastructure, fed by the 15min delay queue
	if err := SetupRetryableQueue(ch, ReservationToPaymentTimeoutQueue,
		ReservationToPaymentTimeoutRetryQueue, ReservationToPaymentTimeoutParkingQueue); err != nil {
		return err
	}
	if err := SetupDelayQueue(ch, ReservationToPaymentDelayQueue, ReservationToPaymentTimeoutExchange,
		ReservationToPaymentTimeoutQueue, ReservationToPaymentTimeoutRoutingKey); err != nil {
		return err
	}

	// order-creation queue, with retry infrastructure
	if err := SetupRetryableQueue(ch, PaymentToOrderImmediateQueue,
		PaymentToOrderRetryQueue, PaymentToOrderParkingQueue); err != nil {
		return err
	}

	return nil
}

func NewMQConn(url string) (*amqp.Connection, error) {
	conn, err := amqp.Dial(url)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func NewChannel(conn *amqp.Connection) (*amqp.Channel, error) {
	ch, err := conn.Channel()
	if err != nil {
		return nil, err
	}
	return ch, nil
}

// NewConfirmingChannel creates a channel with publisher confirm mode enabled.
// Use it together with the SendXxxWithConfirm functions when losing a message
// would leave the system inconsistent. A channel in confirm mode can still be
// used for normal (unconfirmed) publishes.
func NewConfirmingChannel(conn *amqp.Connection) (*amqp.Channel, error) {
	ch, err := NewChannel(conn)
	if err != nil {
		return nil, err
	}
	if err := ch.Confirm(false); err != nil {
		ch.Close()
		return nil, fmt.Errorf("failed to enable publisher confirm mode: %w", err)
	}
	return ch, nil
}

func SetupImmediateQueue(ch *amqp.Channel, immediateQueueName string) error {
	_, err := ch.QueueDeclare(immediateQueueName, true, false, false, false, nil)
	return err
}

// SetupRetryableQueue declares a consumer queue plus its bounded-retry
// infrastructure:
//
//	main queue --nack--> RetryExchange --> retry queue --RetryTTLMs--> main queue
//
// Messages that exhaust MaxRetryCount are parked by the consumer into the
// parking-lot queue (see NackWithRetry). The retry queue's TTL provides
// backoff so a persistently failing message doesn't hot-loop the consumer.
func SetupRetryableQueue(ch *amqp.Channel, mainQueue, retryQueue, parkingQueue string) error {
	if err := ch.ExchangeDeclare(RetryExchange, "direct", true, false, false, false, nil); err != nil {
		return err
	}

	mainArgs := amqp.Table{
		"x-dead-letter-exchange":    RetryExchange,
		"x-dead-letter-routing-key": retryQueue,
	}
	if _, err := ch.QueueDeclare(mainQueue, true, false, false, false, mainArgs); err != nil {
		return err
	}

	retryArgs := amqp.Table{
		"x-message-ttl": RetryTTLMs,
		// the default exchange routes by queue name: expired messages land
		// back on the main queue
		"x-dead-letter-exchange":    "",
		"x-dead-letter-routing-key": mainQueue,
	}
	if _, err := ch.QueueDeclare(retryQueue, true, false, false, false, retryArgs); err != nil {
		return err
	}
	if err := ch.QueueBind(retryQueue, retryQueue, RetryExchange, false, nil); err != nil {
		return err
	}

	// terminal queue for messages that exhausted retries; inspected manually
	_, err := ch.QueueDeclare(parkingQueue, true, false, false, false, nil)
	return err
}

// the delay queue consists three part: delay queue, timeout exchange. Message is first sent
// to delay queue and waiting for ttl time, then the message become dead-message and get into exchanger.
// The exchanger will transfer the message to an immediateQueue(timeoutQueue here) which can be consumed
// by the consumer. The timeoutQueue itself must already exist — declare it with
// SetupRetryableQueue so its consumers also get bounded retries.
func SetupDelayQueue(ch *amqp.Channel, delayQueueName, timeoutExchangeName, timeoutQueueName string, timeoutRoutingKey string) error {
	delayArgs := amqp.Table{
		"x-message-ttl":             int32(15 * 60 * 1000), // 15 mins
		"x-dead-letter-exchange":    timeoutExchangeName,
		"x-dead-letter-routing-key": timeoutRoutingKey,
	}

	if _, err := ch.QueueDeclare(
		delayQueueName, true, false, false, false, delayArgs); err != nil {
		return err
	}

	if err := ch.ExchangeDeclare(timeoutExchangeName, "direct", true, false, false, false, nil); err != nil {
		return err
	}

	return ch.QueueBind(timeoutQueueName, timeoutRoutingKey, timeoutExchangeName, false, nil)
}
