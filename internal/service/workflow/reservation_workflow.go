package workflow

import (
	"context"
	"log"
	"time"

	"github.com/qs-lzh/flash-sale/internal/mq"
	"github.com/qs-lzh/flash-sale/internal/service/domain"

	amqp "github.com/rabbitmq/amqp091-go"
)

// publishConfirmTimeout bounds how long Reserve waits for a single broker
// confirm before treating the publish as failed and rolling back.
const publishConfirmTimeout = 5 * time.Second

// confirmChannelPoolSize 是热路径上复用的 confirm channel 数量。
// 稳态并发远超此值也没关系：池空时临时新建，只是退化为旧的每请求创建模式。
const confirmChannelPoolSize = 16

type ReservationWorkflow struct {
	ReservationService domain.ReservationService
	MQConn             *amqp.Connection
	ChPool             *mq.ChannelPool
}

func NewReservationWorkflow(reservationService domain.ReservationService, mqConn *amqp.Connection) *ReservationWorkflow {
	return &ReservationWorkflow{
		ReservationService: reservationService,
		MQConn:             mqConn,
		ChPool:             mq.NewConfirmChannelPool(mqConn, confirmChannelPoolSize),
	}
}

type sendFunc func(ctx context.Context, ch *amqp.Channel, queueName string, message any) error

func (w *ReservationWorkflow) Reserve(userID, showtimeID uint) error {
	reservationID, err := w.ReservationService.Reserve(userID, showtimeID)
	if err != nil {
		return err
	}

	// 从池中借用 confirm channel（用完归还），避免每请求一次 channel.open 网络往返
	ch, err := w.ChPool.Get()
	if err != nil {
		w.rollbackReservation(reservationID, showtimeID, userID)
		return err
	}
	defer w.ChPool.Put(ch)

	// 先发延时消息、再发即时消息：即时消息才是触发订单流转（支付/入库）的那条。
	// 只要它最后发，任何一步失败时订单都还没有被任何消费者看到过，
	// 此时回滚 Redis 永远是安全的；而残留（已发出的延时）消息会被
	// MarkTicketAsTimeout 幂等忽略。
	if err := w.sendWithConfirm(ch, mq.SendTimeoutMessageWithConfirm,
		mq.ReservationToPaymentDelayQueue,
		mq.ReservationToPaymentDelayMessage{
			ReservationID: reservationID,
		}); err != nil {
		w.rollbackReservation(reservationID, showtimeID, userID)
		return err
	}

	if err := w.sendWithConfirm(ch, mq.SendImmediateMessageWithConfirm,
		mq.ReservationToPaymentImmediateQueue,
		mq.ReservationToPaymentImmediateMessage{
			ReservationID: reservationID,
			Price:         1,
		}); err != nil {
		w.rollbackReservation(reservationID, showtimeID, userID)
		return err
	}

	return nil
}

func (w *ReservationWorkflow) sendWithConfirm(ch *amqp.Channel, send sendFunc, queueName string, message any) error {
	ctx, cancel := context.WithTimeout(context.Background(), publishConfirmTimeout)
	defer cancel()
	return send(ctx, ch, queueName, message)
}

// rollbackReservation rolls back the reservation in Redis when a message
// fails to reach the broker: the ticket is returned to the pool and the
// user's purchase eligibility is released, so retrying the request works.
// If the rollback itself fails the reservation is stranded; the leftover
// delay message (15min) and the reconcile workflow clean it up later.
func (w *ReservationWorkflow) rollbackReservation(reservationID, showtimeID, userID uint) {
	if err := w.ReservationService.Cancel(reservationID, showtimeID, userID); err != nil {
		log.Printf("Failed to roll back reservation %d (showtime %d, user %d): %v",
			reservationID, showtimeID, userID, err)
	}
}
