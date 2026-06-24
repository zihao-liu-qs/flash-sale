package mq

import (
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
)

// Re-export message types
type PaymentToOrderImmediateMessage = pkgmq.PaymentToOrderImmediate

var (
	NewMQConn     = pkgmq.NewConn
	NewChannel    = pkgmq.NewChannel
	InitQueues    = pkgmq.InitQueues
	NackWithRetry = pkgmq.NackWithRetry
)
