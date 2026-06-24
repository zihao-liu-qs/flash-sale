//	@title			Flash Sale API
//	@version		1.0
//	@description	Flash sale microservice API documentation
//	@host			localhost:4000
//	@BasePath		/
//	@securityDefinitions.apikey	BearerAuth
//	@in							header
//	@name						Authorization

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
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"

	"github.com/qs-lzh/flash-sale/pkg/auth"
	"github.com/qs-lzh/flash-sale/pkg/discovery"
	"github.com/qs-lzh/flash-sale/pkg/logger"
	"github.com/qs-lzh/flash-sale/pkg/metrics"
	"github.com/qs-lzh/flash-sale/pkg/resilience"
	"github.com/qs-lzh/flash-sale/pkg/tracing"
	"github.com/qs-lzh/flash-sale/services/reservation/internal/cache"
	"github.com/qs-lzh/flash-sale/services/reservation/internal/config"
	"github.com/qs-lzh/flash-sale/services/reservation/internal/handler"
	"github.com/qs-lzh/flash-sale/services/reservation/internal/model"
	"github.com/qs-lzh/flash-sale/services/reservation/internal/mq"
	"github.com/qs-lzh/flash-sale/services/reservation/internal/outbox"
	"github.com/qs-lzh/flash-sale/services/reservation/internal/repository"
	_ "github.com/qs-lzh/flash-sale/services/reservation/docs"
)

func main() {
	if err := logger.Init("reservation"); err != nil {
		panic(err)
	}
	defer logger.Sync()

	tp, err := tracing.Init("reservation", "localhost:4318")
	if err != nil {
		logger.Log.Fatalf("Failed to init tracing: %v", err)
	}
	defer func() { _ = tp.Shutdown(context.Background()) }()

	resilience.Init()

	cfg, err := config.LoadConfig()

	// Consul service registration
	consulClient, _ := discovery.NewClient(os.Getenv("CONSUL_ADDR"))
	var consulReg *discovery.ServiceRegistration
	if consulClient != nil {
		consulReg, _ = discovery.Register(consulClient, "reservation", discovery.ParsePort(cfg.Addr))
		defer consulReg.Deregister()
	}
	if err != nil {
		logger.Log.Fatalf("Failed to load config: %v", err)
	}

	db, err := gorm.Open(postgres.Open(cfg.DatabaseDSN), &gorm.Config{})
	if err != nil {
		logger.Log.Fatalf("Failed to open database: %v", err)
	}

	if err := db.AutoMigrate(&model.Showtime{}, &model.Reservation{}, &model.Outbox{}); err != nil {
		logger.Log.Fatalf("Failed to auto migrate: %v", err)
	}

	c, err := cache.NewRedisCache(cfg.CacheURL)
	if err != nil {
		logger.Log.Fatalf("Failed to create cache: %v", err)
	}

	mqConn, err := mq.NewMQConn(cfg.MQURL)
	if err != nil {
		logger.Log.Fatalf("Failed to create rabbit mq: %v", err)
	}
	defer mqConn.Close()

	showtimes, err := getAllShowtimes(db)
	if err != nil {
		logger.Log.Fatalf("Failed to get showtimes: %v", err)
	}
	ticketsMap := make(map[uint]int)
	for _, st := range showtimes {
		ticketsMap[st.ID] = 100
	}
	if err := c.Init(ticketsMap); err != nil {
		logger.Log.Fatalf("Failed to init cache: %v", err)
	}

	repo := repository.New(db)

	// Start saga state consumer to receive PAID/TIMEOUT notifications
	if err := handler.StartSagaConsumer(mqConn, repo, c); err != nil {
		logger.Log.Fatalf("Failed to start saga consumer: %v", err)
	}

	// Outbox dispatcher: reliable async MQ publishing
	outboxStore := outbox.NewStore(db)
	outboxDispatcher := outbox.NewDispatcher(db, mqConn)
	outboxDispatcher.Start()

	svc := handler.New(c, outboxStore, repo)

	r := gin.New()
	r.Use(otelgin.Middleware("reservation"))
	r.Use(handler.MetricsMiddleware())
	r.GET("/health", func(ctx *gin.Context) { ctx.String(http.StatusOK, "ok") })
	r.GET("/ready", func(ctx *gin.Context) { ctx.String(http.StatusOK, "ready") })
	r.GET("/metrics", gin.WrapH(metrics.Handler()))
	r.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	api := r.Group("/")
	api.Use(auth.GinMiddleware())
	api.Use(handler.RateLimitMiddleware(10000))
	api.POST("/reserve", svc.HandleReserve)

	srv := &http.Server{Addr: cfg.Addr, Handler: r}

	go func() {
		logger.Log.Infof("Reservation service listening on %s", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Log.Fatalf("Failed to start server: %v", err)
		}
	}()

	logger.Log.Info("Reservation service started, saga consumer active")

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Log.Info("Reservation service shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
	logger.Log.Info("Reservation service stopped")
}

func getAllShowtimes(db *gorm.DB) ([]model.Showtime, error) {
	var showtimes []model.Showtime
	if err := db.Find(&showtimes).Error; err != nil {
		return nil, err
	}
	return showtimes, nil
}
