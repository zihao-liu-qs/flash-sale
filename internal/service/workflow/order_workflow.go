package workflow

import (
	"encoding/json"
	"log"
	"sync"

	"github.com/qs-lzh/flash-sale/internal/cache"
	"github.com/qs-lzh/flash-sale/internal/mq"
	"github.com/qs-lzh/flash-sale/internal/service/domain"

	amqp "github.com/rabbitmq/amqp091-go"
)

// OrderWorkflow consumes order-creation messages and persists paid
// reservations to PostgreSQL. CreateOrderFromReservation is idempotent
// (the reservation id doubles as the order's primary key), so retried and
// duplicated messages are safe.
type OrderWorkflow struct {
	cache        *cache.RedisCache
	orderService domain.OrderService

	publishCh *amqp.Channel
	publishMu sync.Mutex
}

func NewOrderWorkflow(cache *cache.RedisCache, orderService domain.OrderService) *OrderWorkflow {
	return &OrderWorkflow{
		cache:        cache,
		orderService: orderService,
	}
}

func (w *OrderWorkflow) Start(mqConn *amqp.Connection) error {
	ch, err := mq.NewChannel(mqConn)
	if err != nil {
		return err
	}
	w.publishCh = ch

	if err := w.ConsumeOrderCreation(mqConn); err != nil {
		return err
	}
	return nil
}

func (w *OrderWorkflow) ConsumeOrderCreation(conn *amqp.Connection) error {
	ch, err := mq.NewChannel(conn)
	if err != nil {
		return err
	}

	msgs, err := ch.Consume(mq.PaymentToOrderImmediateQueue, "", false, false, false, false, nil)
	if err != nil {
		return err
	}

	go func() {
		for msg := range msgs {
			if err := w.handleOrderCreation(msg); err != nil {
				log.Printf("Failed to handle order creation: %v", err)
			}
		}
	}()

	return nil
}

func (w *OrderWorkflow) handleOrderCreation(msg amqp.Delivery) error {
	var message mq.PaymentToOrderImmediateMessage
	if err := json.Unmarshal(msg.Body, &message); err != nil {
		log.Printf("Invalid order message (%v), routing to retry/parking", err)
		w.retryOrPark(msg)
		return err
	}

	if err := w.orderService.CreateOrderFromReservation(message.ReservationID); err != nil {
		w.retryOrPark(msg)
		return err
	}

	msg.Ack(false)

	return nil
}

func (w *OrderWorkflow) retryOrPark(msg amqp.Delivery) {
	w.publishMu.Lock()
	defer w.publishMu.Unlock()
	if err := mq.NackWithRetry(msg, w.publishCh, mq.PaymentToOrderParkingQueue, mq.MaxRetryCount); err != nil {
		log.Printf("Failed to park message: %v", err)
	}
}
