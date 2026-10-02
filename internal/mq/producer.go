package mq

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

func SendImmediateMessage(ch *amqp.Channel, queueName string, message any) error {
	body, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	err = ch.PublishWithContext(
		context.Background(),
		"",
		queueName,
		false,
		false,
		amqp.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent,
			Body:         body,
			Timestamp:    time.Now(),
		},
	)
	if err != nil {
		return fmt.Errorf("failed to publish message to queue %s: %w", queueName, err)
	}

	return nil
}

func SendTimeoutMessage(ch *amqp.Channel, delayQueueName string, message any) error {
	body, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("Failed to marshal message: %w", err)
	}

	return ch.PublishWithContext(
		context.Background(),
		"",
		delayQueueName,
		false,
		false,
		amqp.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent,
			Body:         body,
			Timestamp:    time.Now(),
		},
	)
}

// SendImmediateMessageWithConfirm is like SendImmediateMessage, but blocks
// until the broker confirms (or nacks) the message, so a lost message is
// reported to the caller instead of going unnoticed. The channel must have
// been created with NewConfirmingChannel.
func SendImmediateMessageWithConfirm(ctx context.Context, ch *amqp.Channel, queueName string, message any) error {
	return publishWithConfirm(ctx, ch, queueName, message)
}

// SendTimeoutMessageWithConfirm is like SendTimeoutMessage with broker
// confirmation. The channel must have been created with NewConfirmingChannel.
func SendTimeoutMessageWithConfirm(ctx context.Context, ch *amqp.Channel, delayQueueName string, message any) error {
	return publishWithConfirm(ctx, ch, delayQueueName, message)
}

func publishWithConfirm(ctx context.Context, ch *amqp.Channel, queueName string, message any) error {
	body, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	dconf, err := ch.PublishWithDeferredConfirmWithContext(
		ctx,
		"",
		queueName,
		false,
		false,
		amqp.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent,
			Body:         body,
			Timestamp:    time.Now(),
		},
	)
	if err != nil {
		return fmt.Errorf("failed to publish message to queue %s: %w", queueName, err)
	}

	ack, err := dconf.WaitContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to wait for publish confirm from queue %s: %w", queueName, err)
	}
	if !ack {
		return fmt.Errorf("message to queue %s was nacked by broker", queueName)
	}

	return nil
}
