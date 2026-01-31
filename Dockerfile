FROM golang:1.25-alpine3.22 AS builder

# 第一阶段，编译go

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN go build -o bin/main ./cmd/flash-sale/main.go

# 第二个阶段，配置运行环境

FROM alpine:3.14

#项目中是http，不需要ca-certificates

WORKDIR /app

# 复制编译好的文件
COPY --from=builder /app/bin/main .

# Go服务端口
EXPOSE 4000

CMD ["./main"]
