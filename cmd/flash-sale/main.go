package main

import (
	"log"
	"net/http"
	_ "net/http/pprof"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/qs-lzh/flash-sale/config"
	"github.com/qs-lzh/flash-sale/internal/app"
	"github.com/qs-lzh/flash-sale/internal/cache"
	"github.com/qs-lzh/flash-sale/internal/handler"
	"github.com/qs-lzh/flash-sale/internal/model"
	"github.com/qs-lzh/flash-sale/internal/mq"
)

func main() {
	cfg, err := config.LoadConfig()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	db, err := gorm.Open(postgres.Open(cfg.DatabaseDSN), &gorm.Config{})
	if err != nil {
		log.Fatalf("Failed to open gorm.DB: %v", err)
	}
	if err = initDB(db); err != nil {
		log.Fatalf("Failed to init DB: %v", err)
	}

	cache, err := cache.NewRedisCache(cfg.CacheURL)
	if err != nil {
		log.Fatalf("Failed to create cache: %v", err)
	}

	mqConn, err := mq.NewMQConn(cfg.MQURL)
	if err != nil {
		log.Fatalf("Failed to create rabbit mq: %v", err)
	}

	app := app.New(cfg, db, cache, mqConn)
	defer app.Close()

	if err := app.Init(); err != nil {
		log.Fatalf("Failed to init app: %v", err)
	}

	// pprof 性能分析端点（/debug/pprof/），独立于业务 HTTP 服务，
	// 供压测时采集 CPU/goroutine profile 定位热点
	go func() {
		log.Printf("pprof listening on %s", cfg.PprofAddr)
		if err := http.ListenAndServe(cfg.PprofAddr, nil); err != nil {
			log.Printf("pprof server exited: %v", err)
		}
	}()

	r := gin.New()

	reserveHandler := handler.NewReserveHandler(app)

	r.POST("/reserve", reserveHandler.HandleReserve)

	r.Run(cfg.Addr)
}

func initDB(db *gorm.DB) error {
	if err := db.Migrator().DropTable(
		&model.Order{},
		&model.Showtime{},
		&model.Movie{},
		&model.User{},
	); err != nil {
		return err
	}

	if err := db.Migrator().AutoMigrate(
		&model.User{},
		&model.Movie{},
		&model.Showtime{},
		&model.Order{},
	); err != nil {
		return err
	}

	return nil
}
