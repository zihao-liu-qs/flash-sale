package resilience

import (
	"github.com/sony/gobreaker/v2"
)

var (
	RedisCB    *gobreaker.TwoStepCircuitBreaker[any]
	PostgresCB *gobreaker.TwoStepCircuitBreaker[any]
)

func Init() {
	settings := gobreaker.Settings{
		Name:        "redis",
		MaxRequests: 3,
		Interval:    0,
		Timeout:     30,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			return counts.TotalFailures >= 5
		},
	}
	RedisCB = gobreaker.NewTwoStepCircuitBreaker[any](settings)

	settings.Name = "postgres"
	PostgresCB = gobreaker.NewTwoStepCircuitBreaker[any](settings)
}
