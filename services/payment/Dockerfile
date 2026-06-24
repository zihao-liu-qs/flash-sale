FROM golang:1.25-alpine AS builder

WORKDIR /app

COPY pkg/ ./pkg/
COPY services/payment/ ./services/payment/

WORKDIR /app/services/payment
RUN go mod tidy && go build -o bin/payment ./cmd/main.go

FROM alpine:3.14

WORKDIR /app
COPY --from=builder /app/services/payment/bin/payment .

CMD ["./payment"]
