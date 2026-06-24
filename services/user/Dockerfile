FROM golang:1.25-alpine AS builder

WORKDIR /app

COPY pkg/ ./pkg/
COPY services/user/ ./services/user/

WORKDIR /app/services/user
RUN go mod tidy && go build -o bin/user ./cmd/main.go

FROM alpine:3.14

WORKDIR /app
COPY --from=builder /app/services/user/bin/user .

EXPOSE 4003
CMD ["./user"]
