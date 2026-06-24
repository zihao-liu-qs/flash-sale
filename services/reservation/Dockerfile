FROM golang:1.25-alpine AS builder

WORKDIR /app

COPY pkg/ ./pkg/
COPY services/reservation/ ./services/reservation/

WORKDIR /app/services/reservation
RUN go mod tidy && go build -o bin/reservation ./cmd/main.go

FROM alpine:3.14

WORKDIR /app
COPY --from=builder /app/services/reservation/bin/reservation .

EXPOSE 4000
CMD ["./reservation"]
