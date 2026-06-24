package handler

import (
	"encoding/json"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/qs-lzh/flash-sale/pkg/logger"
	"github.com/qs-lzh/flash-sale/services/reservation/internal/cache"
	"github.com/qs-lzh/flash-sale/services/reservation/internal/model"
	"github.com/qs-lzh/flash-sale/services/reservation/internal/mq"
	"github.com/qs-lzh/flash-sale/services/reservation/internal/repository"
)

func StartSagaConsumer(mqConn *amqp.Connection, repo *repository.Repo, redisCache *cache.RedisCache) error {
	ch, err := mq.NewChannel(mqConn)
	if err != nil {
		return err
	}

	msgs, err := ch.Consume(mq.SagaStateQueue, "", false, false, false, false, nil)
	if err != nil {
		return err
	}

	go func() {
		for msg := range msgs {
			handleSagaState(msg, repo, redisCache)
		}
	}()
	return nil
}

func handleSagaState(msg amqp.Delivery, repo *repository.Repo, redisCache *cache.RedisCache) {
	var message mq.SagaStateMessage
	if err := json.Unmarshal(msg.Body, &message); err != nil {
		msg.Nack(false, false)
		return
	}

	var state model.SagaState
	switch message.State {
	case "PAID":
		state = model.SagaStatePaid
		// Reservation owns Redis — it performs the state change here.
		if err := redisCache.MarkTicketAsPaid(message.ReservationID); err != nil {
			logger.Log.Errorf("Failed to mark ticket as paid (res=%d): %v", message.ReservationID, err)
			mq.NackWithRetry(msg)
			return
		}
	case "TIMEOUT":
		state = model.SagaStateTimeout
		// Reservation releases the ticket back to the pool.
		if err := redisCache.MarkTicketAsTimeout(message.ReservationID); err != nil {
			logger.Log.Errorf("Failed to mark ticket as timeout (res=%d): %v", message.ReservationID, err)
			mq.NackWithRetry(msg)
			return
		}
	default:
		msg.Ack(false)
		return
	}

	if err := repo.UpdateState(message.ReservationID, state); err != nil {
		logger.Log.Errorf("Failed to update saga state (res=%d, state=%s): %v",
			message.ReservationID, state, err)
		mq.NackWithRetry(msg)
		return
	}

	logger.Log.Infof("Saga state updated: reservation %d -> %s", message.ReservationID, state)
	msg.Ack(false)
}
