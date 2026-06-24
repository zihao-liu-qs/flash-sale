FROM golang:1.25-alpine AS builder

WORKDIR /app

COPY pkg/ ./pkg/
COPY services/order/ ./services/order/

WORKDIR /app/services/order
RUN go mod tidy && go build -o bin/order ./cmd/main.go

FROM alpine:3.14

WORKDIR /app
COPY --from=builder /app/services/order/bin/order .

CMD ["./order"]
