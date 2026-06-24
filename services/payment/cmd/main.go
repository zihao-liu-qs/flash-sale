package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/qs-lzh/flash-sale/pkg/discovery"
	"github.com/qs-lzh/flash-sale/pkg/health"
	"github.com/qs-lzh/flash-sale/pkg/logger"
	"github.com/qs-lzh/flash-sale/pkg/tracing"
	"github.com/qs-lzh/flash-sale/services/payment/internal/config"
	"github.com/qs-lzh/flash-sale/services/payment/internal/mq"
	"github.com/qs-lzh/flash-sale/services/payment/internal/service"
)

func main() {
	if err := logger.Init("payment"); err != nil {
		panic(err)
	}
	defer logger.Sync()

	tp, err := tracing.Init("payment", "localhost:4318")
	if err != nil {
		logger.Log.Fatalf("Failed to init tracing: %v", err)
	}
	defer func() { _ = tp.Shutdown(context.Background()) }()

	cfg, err := config.LoadConfig()
	if err != nil {
		logger.Log.Fatalf("Failed to load config: %v", err)
	}

	consulClient, _ := discovery.NewClient(os.Getenv("CONSUL_ADDR"))
	var consulReg *discovery.ServiceRegistration
	if consulClient != nil {
		consulReg, _ = discovery.Register(consulClient, "payment", 4001)
		defer consulReg.Deregister()
	}

	mqConn, err := mq.NewMQConn(cfg.MQURL)
	if err != nil {
		logger.Log.Fatalf("Failed to create rabbit mq: %v", err)
	}

	if err := mq.InitQueues(mqConn); err != nil {
		logger.Log.Fatalf("Failed to init mq queues: %v", err)
	}

	svc := service.New(mqConn)
	if err := svc.Start(); err != nil {
		logger.Log.Fatalf("Failed to start payment service: %v", err)
	}

	healthSrv := health.New(":4001")
	go func() { _ = healthSrv.Start() }()
	healthSrv.MarkReady()

	logger.Log.Info("Payment service started, waiting for messages...")

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Log.Info("Payment service shutting down...")
	healthSrv.Shutdown()
	mqConn.Close()
	logger.Log.Info("Payment service stopped")
}
