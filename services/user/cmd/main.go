package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/gin-gonic/gin"

	"github.com/qs-lzh/flash-sale/pkg/discovery"
	"github.com/qs-lzh/flash-sale/pkg/logger"
	"github.com/qs-lzh/flash-sale/services/user/internal/config"
	"github.com/qs-lzh/flash-sale/services/user/internal/handler"
	"github.com/qs-lzh/flash-sale/services/user/internal/model"
)

func main() {
	if err := logger.Init("user"); err != nil {
		panic(err)
	}
	defer logger.Sync()

	cfg, err := config.LoadConfig()
	if err != nil {
		logger.Log.Fatalf("Failed to load config: %v", err)
	}

	consulClient, _ := discovery.NewClient(os.Getenv("CONSUL_ADDR"))
	var consulReg *discovery.ServiceRegistration
	if consulClient != nil {
		consulReg, _ = discovery.Register(consulClient, "user", discovery.ParsePort(cfg.Addr))
		defer consulReg.Deregister()
	}

	db, err := gorm.Open(postgres.Open(cfg.DatabaseDSN), &gorm.Config{})
	if err != nil {
		logger.Log.Fatalf("Failed to open database: %v", err)
	}

	if err := db.AutoMigrate(&model.User{}); err != nil {
		logger.Log.Fatalf("Failed to auto migrate: %v", err)
	}

	svc := handler.New(db)

	r := gin.New()
	r.POST("/register", svc.Register)
	r.POST("/login", svc.Login)
	r.GET("/health", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	srv := &http.Server{Addr: cfg.Addr, Handler: r}
	go func() {
		logger.Log.Infof("User service listening on %s", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Log.Fatalf("Failed to start server: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Log.Info("User service shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
	logger.Log.Info("User service stopped")
}
