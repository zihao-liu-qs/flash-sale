package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/qs-lzh/flash-sale/pkg/discovery"
	"github.com/qs-lzh/flash-sale/pkg/health"
	"github.com/qs-lzh/flash-sale/pkg/logger"
	"github.com/qs-lzh/flash-sale/pkg/resilience"
	"github.com/qs-lzh/flash-sale/pkg/tracing"
	"github.com/qs-lzh/flash-sale/services/order/internal/config"
	"github.com/qs-lzh/flash-sale/services/order/internal/model"
	"github.com/qs-lzh/flash-sale/services/order/internal/mq"
	"github.com/qs-lzh/flash-sale/services/order/internal/service"
)

func main() {
	if err := logger.Init("order"); err != nil {
		panic(err)
	}
	defer logger.Sync()

	tp, err := tracing.Init("order", "localhost:4318")
	if err != nil {
		logger.Log.Fatalf("Failed to init tracing: %v", err)
	}
	defer func() { _ = tp.Shutdown(context.Background()) }()

	resilience.Init()

	cfg, err := config.LoadConfig()
	if err != nil {
		logger.Log.Fatalf("Failed to load config: %v", err)
	}

	consulClient, _ := discovery.NewClient(os.Getenv("CONSUL_ADDR"))
	var consulReg *discovery.ServiceRegistration
	if consulClient != nil {
		consulReg, _ = discovery.Register(consulClient, "order", 4002)
		defer consulReg.Deregister()
	}

	db, err := gorm.Open(postgres.Open(cfg.DatabaseDSN), &gorm.Config{})
	if err != nil {
		logger.Log.Fatalf("Failed to open database: %v", err)
	}

	if err := db.AutoMigrate(&model.Order{}); err != nil {
		logger.Log.Fatalf("Failed to auto migrate: %v", err)
	}

	mqConn, err := mq.NewMQConn(cfg.MQURL)
	if err != nil {
		logger.Log.Fatalf("Failed to create rabbit mq: %v", err)
	}

	if err := mq.InitQueues(mqConn); err != nil {
		logger.Log.Fatalf("Failed to init mq queues: %v", err)
	}

	svc := service.New(db)
	if err := svc.Start(mqConn); err != nil {
		logger.Log.Fatalf("Failed to start order service: %v", err)
	}

	healthSrv := health.New(":4002")
	go func() { _ = healthSrv.Start() }()
	healthSrv.MarkReady()

	logger.Log.Info("Order service started, waiting for messages...")

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Log.Info("Order service shutting down...")
	healthSrv.Shutdown()
	mqConn.Close()
	logger.Log.Info("Order service stopped")
}
