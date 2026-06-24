.PHONY: run-reservation run-payment run-order test

run-reservation:
	go run ./services/reservation/cmd/main.go

run-payment:
	go run ./services/payment/cmd/main.go

run-order:
	go run ./services/order/cmd/main.go

test:
	go test -v ./test/concurrent_test.go
