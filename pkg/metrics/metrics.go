package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	HTTPRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "http_requests_total", Help: "Total HTTP requests"},
		[]string{"method", "path", "status"},
	)
	HTTPRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{Name: "http_request_duration_seconds", Help: "HTTP request latency"},
		[]string{"method", "path"},
	)

	MQConsumedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "mq_consumed_total", Help: "Total MQ messages consumed"},
		[]string{"queue", "status"},
	)

	ReservationTotal = prometheus.NewCounter(
		prometheus.CounterOpts{Name: "reservation_total", Help: "Total reservation attempts"},
	)
	ReservationSuccess = prometheus.NewCounter(
		prometheus.CounterOpts{Name: "reservation_success", Help: "Successful reservations"},
	)
	ReservationFailed = prometheus.NewCounter(
		prometheus.CounterOpts{Name: "reservation_failed", Help: "Failed reservations"},
	)

	PaymentTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "payment_total", Help: "Total payment attempts"},
		[]string{"result"},
	)
	PaymentDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{Name: "payment_duration_seconds", Help: "Payment processing latency"},
	)

	OrderCreatedTotal = prometheus.NewCounter(
		prometheus.CounterOpts{Name: "order_created_total", Help: "Total orders created"},
	)
)

func init() {
	prometheus.MustRegister(
		HTTPRequestsTotal, HTTPRequestDuration,
		MQConsumedTotal,
		ReservationTotal, ReservationSuccess, ReservationFailed,
		PaymentTotal, PaymentDuration,
		OrderCreatedTotal,
	)
}

func Handler() http.Handler {
	return promhttp.Handler()
}
