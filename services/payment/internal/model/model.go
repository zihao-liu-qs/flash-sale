package model

type ReservationToPaymentImmediateMessage struct {
	ReservationID uint `json:"reservation_id"`
	Price         int  `json:"price"`
}

type ReservationToPaymentDelayMessage struct {
	ReservationID uint `json:"reservation_id"`
}

type PaymentToOrderImmediateMessage struct {
	ReservationID uint `json:"reservation_id"`
}
