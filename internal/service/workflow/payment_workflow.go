package workflow

import (
	"context"
	"encoding/json"
	"log"
	"sync"

	"github.com/qs-lzh/flash-sale/internal/cache"
	"github.com/qs-lzh/flash-sale/internal/mq"
	"github.com/qs-lzh/flash-sale/internal/service/domain"

	amqp "github.com/rabbitmq/amqp091-go"
)

// PaymentWorkflow consumes payment and timeout messages.
//
// publishCh/publishMu: amqp channels are not safe for concurrent use, and
// publish confirms are matched by delivery tag, so every publish from the
// concurrent message handlers is serialized through the mutex.
type PaymentWorkflow struct {
	paymentService domain.PaymentService
	mqConn         *amqp.Connection

	publishCh *amqp.Channel
	publishMu sync.Mutex
}

func NewPaymentWorkflow(paymentService domain.PaymentService, mqConn *amqp.Connection) *PaymentWorkflow {
	return &PaymentWorkflow{
		paymentService: paymentService,
		mqConn:         mqConn,
	}
}

func (w *PaymentWorkflow) Start(mqConn *amqp.Connection) error {
	ch, err := mq.NewConfirmingChannel(mqConn)
	if err != nil {
		return err
	}
	w.publishCh = ch

	if err := w.ConsumePaymentCreate(mqConn); err != nil {
		return err
	}
	if err := w.ConsumePaymentTimeout(mqConn); err != nil {
		return err
	}

	return nil
}

func (w *PaymentWorkflow) ConsumePaymentCreate(conn *amqp.Connection) error {
	ch, err := mq.NewChannel(conn)
	if err != nil {
		return err
	}

	msgs, err := ch.Consume(mq.ReservationToPaymentImmediateQueue, "", false, false, false, false, nil)
	if err != nil {
		return err
	}

	go func() {
		for msg := range msgs {
			go w.handlePaymentMessage(msg)
		}
	}()

	return nil
}

// handlePaymentMessage 的顺序是关键设计（at-least-once 下的幂等消费）：
//
//	MarkPaid（幂等）→ 发入库消息（confirm）→ 全部成功才 Ack
//
// 发入库消息失败则整个支付消息走有限重试；重试时 MarkPaid 命中 PaidAlready，
// 不会重复改状态，只是补发上次没发出去的入库消息。
func (w *PaymentWorkflow) handlePaymentMessage(msg amqp.Delivery) {
	var message mq.ReservationToPaymentImmediateMessage
	if err := json.Unmarshal(msg.Body, &message); err != nil {
		log.Printf("Invalid payment message (%v), routing to retry/parking", err)
		w.retryOrPark(msg, mq.ReservationToPaymentParkingQueue)
		return
	}

	status, err := w.paymentService.StartMockPay(message.ReservationID)
	if err != nil {
		log.Printf("Failed to pay reservation %d: %v", message.ReservationID, err)
		w.retryOrPark(msg, mq.ReservationToPaymentParkingQueue)
		return
	}
	if status == cache.PaidNotFound {
		// 订单已被回滚（如 confirm 回执丢失后的残留消息），直接忽略
		msg.Ack(false)
		return
	}

	if err := w.publishOrderCreation(message.ReservationID); err != nil {
		log.Printf("Failed to send order-creation message for reservation %d: %v",
			message.ReservationID, err)
		w.retryOrPark(msg, mq.ReservationToPaymentParkingQueue)
		return
	}

	msg.Ack(false)
}

func (w *PaymentWorkflow) publishOrderCreation(reservationID uint) error {
	ctx, cancel := context.WithTimeout(context.Background(), publishConfirmTimeout)
	defer cancel()

	w.publishMu.Lock()
	defer w.publishMu.Unlock()
	return mq.SendImmediateMessageWithConfirm(ctx, w.publishCh,
		mq.PaymentToOrderImmediateQueue,
		mq.PaymentToOrderImmediateMessage{
			ReservationID: reservationID,
		})
}

func (w *PaymentWorkflow) ConsumePaymentTimeout(mqConn *amqp.Connection) error {
	ch, err := mq.NewChannel(mqConn)
	if err != nil {
		return err
	}

	msgs, err := ch.Consume(mq.ReservationToPaymentTimeoutQueue, "", false, false, false, false, nil)
	if err != nil {
		return err
	}

	go func() {
		for msg := range msgs {
			w.handlePaymentTimeout(msg)
		}
	}()

	return nil
}

func (w *PaymentWorkflow) handlePaymentTimeout(msg amqp.Delivery) {
	var message mq.ReservationToPaymentDelayMessage
	if err := json.Unmarshal(msg.Body, &message); err != nil {
		log.Printf("Invalid timeout message (%v), routing to retry/parking", err)
		w.retryOrPark(msg, mq.ReservationToPaymentTimeoutParkingQueue)
		return
	}
	if err := w.paymentService.MarkTimeout(message.ReservationID); err != nil {
		log.Printf("Failed to mark reservation %d as timeout: %v", message.ReservationID, err)
		w.retryOrPark(msg, mq.ReservationToPaymentTimeoutParkingQueue)
		return
	}

	msg.Ack(false)
}

// retryOrPark routes a failed message into the bounded-retry topology, and
// on to the parking lot once retries are exhausted.
func (w *PaymentWorkflow) retryOrPark(msg amqp.Delivery, parkingQueue string) {
	w.publishMu.Lock()
	defer w.publishMu.Unlock()
	if err := mq.NackWithRetry(msg, w.publishCh, parkingQueue, mq.MaxRetryCount); err != nil {
		log.Printf("Failed to park message: %v", err)
	}
}
