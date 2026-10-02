package domain

import (
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/qs-lzh/flash-sale/internal/cache"
	"github.com/qs-lzh/flash-sale/internal/model"
	"github.com/qs-lzh/flash-sale/internal/repository"
)

type OrderService interface {
	CreateOrderFromReservation(reservationID uint) error
}

type orderService struct {
	DB    *gorm.DB
	Cache *cache.RedisCache

	Repo repository.OrderRepo
}

var _ OrderService = (*orderService)(nil)

func NewOrderService(db *gorm.DB, cache *cache.RedisCache, orderRepo repository.OrderRepo) *orderService {
	return &orderService{
		DB:    db,
		Cache: cache,
		Repo:  orderRepo,
	}
}

func (s *orderService) CreateOrderFromReservation(reservationID uint) error {
	return s.DB.Transaction(func(tx *gorm.DB) error {
		// 使用 HGetAll 读取 Hash 类型的 reservation 数据
		reservationData, err := s.Cache.GetReservationInfo(reservationID)
		if err != nil {
			return err
		}

		// 检查订单是否已存在（幂等：消息重复消费时直接返回成功）
		_, err = s.Repo.WithTx(tx).GetByID(reservationID)
		if err == nil {
			return nil // 订单已存在，返回成功
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err // DB 本身出错（连接抖动等），返回错误让消费端重试
		}

		// 从 map 中提取数据（需要类型转换）
		var showtimeID, userID uint
		if val, ok := reservationData["showtime_id"]; ok {
			var id uint64
			fmt.Sscanf(val, "%d", &id)
			showtimeID = uint(id)
		}
		if val, ok := reservationData["user_id"]; ok {
			var id uint64
			fmt.Sscanf(val, "%d", &id)
			userID = uint(id)
		}

		// 写入必须走事务 tx，且错误必须上抛——否则入库失败会被吞掉，
		// 消费端 Ack 后订单永久丢失
		if err := s.Repo.WithTx(tx).Create(&model.Order{
			ID:         reservationID,
			ShowtimeID: showtimeID,
			UserID:     userID,
		}); err != nil {
			return err
		}
		return nil
	})
}
