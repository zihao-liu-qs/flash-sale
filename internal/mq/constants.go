package mq

// Queue names and message definitions

// immediate queue from reservation to payment service
// deliver message to notify payment service to handle a payment of the reservation
const (
	ReservationToPaymentImmediateQueue = "reservation.payment.pay.immediate"
)

type ReservationToPaymentImmediateMessage struct {
	ReservationID uint `json:"reservation_id"`
	Price         int  `json:"price"`
}

// delay queue from reservation to payment service
// deliver message to notify payment service to timeout a payment of the reservation
const (
	ReservationToPaymentDelayQueue        = "reservation.payment.timeout.delay"
	ReservationToPaymentTimeoutQueue      = "reservation.payment.timeout.immediate"
	ReservationToPaymentTimeoutExchange   = "reservation.timeout.exchange"
	ReservationToPaymentTimeoutRoutingKey = "reservation.timeout"
)

type ReservationToPaymentDelayMessage struct {
	ReservationID uint `json:"reservation_id"`
}

// immediate queue from payment to reservation db
// deliver message to notify reservation db to store a paid reservation
const (
	PaymentToOrderImmediateQueue = "payment.order.create.immediate"
)

type PaymentToOrderImmediateMessage struct {
	ReservationID uint `json:"reservation_id"`
}

// retry & parking-lot infrastructure for bounded consumer retries:
//
//	main queue --nack--> RetryExchange --> retry queue --TTL expires--> main queue
//	(retries exhausted)                --> parking-lot queue (manual inspection)
const (
	// RetryExchange carries messages nacked by consumers to their retry queue.
	RetryExchange = "flash-sale.retry.exchange"

	// RetryTTLMs is how long a message waits in the retry queue before being
	// redelivered to its main queue (backoff between attempts).
	RetryTTLMs = int32(10 * 1000)

	// MaxRetryCount is how many times a message is retried before being
	// parked for manual inspection.
	MaxRetryCount = 3
)

const (
	ReservationToPaymentRetryQueue   = "reservation.payment.pay.retry"
	ReservationToPaymentParkingQueue = "reservation.payment.pay.parking"

	ReservationToPaymentTimeoutRetryQueue   = "reservation.payment.timeout.retry"
	ReservationToPaymentTimeoutParkingQueue = "reservation.payment.timeout.parking"

	PaymentToOrderRetryQueue   = "payment.order.create.retry"
	PaymentToOrderParkingQueue = "payment.order.create.parking"
)
