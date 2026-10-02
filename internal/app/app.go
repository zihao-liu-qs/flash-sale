package app

import (
	"github.com/qs-lzh/flash-sale/config"
	"github.com/qs-lzh/flash-sale/internal/cache"
	"github.com/qs-lzh/flash-sale/internal/mq"
	"github.com/qs-lzh/flash-sale/internal/repository"
	"github.com/qs-lzh/flash-sale/internal/service/domain"
	"github.com/qs-lzh/flash-sale/internal/service/workflow"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type App struct {
	Config *config.Config

	DB     *gorm.DB
	Cache  *cache.RedisCache
	Logger *zap.Logger
	MQConn *amqp.Connection

	UserRepo     *repository.UserRepo
	MovieRepo    *repository.MovieRepo
	ShowtimeRepo *repository.ShowtimeRepo

	MovieService       domain.MovieService
	ShowtimeService    domain.ShowtimeService
	ReservationService domain.ReservationService
	OrderService       domain.OrderService
	PaymentService     domain.PaymentService

	ReservationWorkflow *workflow.ReservationWorkflow
	PaymentWorkflow     *workflow.PaymentWorkflow
	OrderWorkflow       *workflow.OrderWorkflow
	ReconcileWorkflow   *workflow.ReconcileWorkflow
}

func New(config *config.Config, db *gorm.DB, cache *cache.RedisCache, mqConn *amqp.Connection) *App {
	movieRepo := repository.NewMovieRepoGorm(db)
	showtimeRepo := repository.NewShowtimeRepoGorm(db)
	orderRepo := repository.NewOrderRepoGorm(db)

	showtimeService := domain.NewShowtimeService(db, showtimeRepo)
	reservationService := domain.NewReservationService(cache)
	orderService := domain.NewOrderService(db, cache, orderRepo)
	movieService := domain.NewMovieService(db, movieRepo, showtimeService)
	paymentService := domain.NewPaymentService(cache)

	reservationWorkflow := workflow.NewReservationWorkflow(reservationService, mqConn)
	paymentWorkflow := workflow.NewPaymentWorkflow(paymentService, mqConn)
	orderWorkflow := workflow.NewOrderWorkflow(cache, orderService)
	reconcileWorkflow := workflow.NewReconcileWorkflow(cache, orderService, paymentService)

	return &App{
		Config:              config,
		DB:                  db,
		Cache:               cache,
		MQConn:              mqConn,
		MovieService:        movieService,
		ShowtimeService:     showtimeService,
		ReservationService:  reservationService,
		OrderService:        orderService,
		PaymentService:      paymentService,
		ReservationWorkflow: reservationWorkflow,
		PaymentWorkflow:     paymentWorkflow,
		OrderWorkflow:       orderWorkflow,
		ReconcileWorkflow:   reconcileWorkflow,
	}
}

func (app *App) Init() error {
	// init redis：库存以 DB 中场次的 TotalTickets 为准（本 demo 启动时清库清缓存，
	// 无已售订单，故剩余库存 = 总票数）
	showtimeIDTicketsMap := make(map[uint]int)
	showtimes, err := app.ShowtimeService.GetAllShowtimes()
	if err != nil {
		return err
	}
	for _, showtime := range showtimes {
		showtimeIDTicketsMap[showtime.ID] = showtime.TotalTickets
	}
	if err := app.Cache.Init(showtimeIDTicketsMap); err != nil {
		return err
	}

	// init rabbit mq
	mq.InitQueues(app.MQConn)

	app.PaymentWorkflow.Start(app.MQConn)
	app.OrderWorkflow.Start(app.MQConn)
	app.ReconcileWorkflow.Start()

	return nil
}

func (app *App) Close() error {
	// 先停对账任务（可能正在读写 DB/Redis），再关 DB 连接
	app.ReconcileWorkflow.Stop()
	sqlDB, err := app.DB.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
