package mq

import (
	"context"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// DeadLetterCount returns how many times the message has been dead-lettered,
// read from the x-death header RabbitMQ maintains automatically. It is what
// makes bounded retries possible without any extra state.
func DeadLetterCount(msg amqp.Delivery) int64 {
	deaths, ok := msg.Headers["x-death"].([]interface{})
	if !ok {
		return 0
	}
	var max int64
	for _, d := range deaths {
		table, ok := d.(amqp.Table)
		if !ok {
			continue
		}
		if count, ok := table["count"].(int64); ok && count > max {
			max = count
		}
	}
	return max
}

// NackWithRetry negatively acknowledges a failed message with bounded retries:
//
//   - below maxRetry: Nack(false, false) — the queue's dead-letter exchange
//     moves the message to its retry queue, which redelivers it to the main
//     queue after RetryTTLMs. No hot loop.
//   - retries exhausted: the message is republished to the parking-lot queue
//     for manual inspection and then acked, so a poison message can never
//     spin forever.
//
// ch is only used to publish to the parking lot; Ack/Nack go through the
// message's own consuming channel.
func NackWithRetry(msg amqp.Delivery, ch *amqp.Channel, parkingQueue string, maxRetry int64) error {
	if DeadLetterCount(msg) < maxRetry {
		return msg.Nack(false, false)
	}

	err := ch.PublishWithContext(
		context.Background(),
		"",
		parkingQueue,
		false,
		false,
		amqp.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent,
			Body:         msg.Body,
			Timestamp:    time.Now(),
		},
	)
	if err != nil {
		// couldn't even park it: requeue and let the next round try again
		return msg.Nack(false, true)
	}
	return msg.Ack(false)
}
