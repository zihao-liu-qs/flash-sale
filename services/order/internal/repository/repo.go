package repository

import (
	"context"

	"gorm.io/gorm"

	"github.com/qs-lzh/flash-sale/services/order/internal/model"
)

type Repo struct {
	db *gorm.DB
}

func New(db *gorm.DB) *Repo {
	return &Repo{db: db}
}

func (r *Repo) WithTx(tx *gorm.DB) *Repo {
	return &Repo{db: tx}
}

func (r *Repo) Create(reservationID, showtimeID, userID uint) error {
	return gorm.G[model.Order](r.db).Create(context.Background(), &model.Order{
		ID:         reservationID,
		ShowtimeID: showtimeID,
		UserID:     userID,
	})
}

func (r *Repo) GetByID(id uint) (*model.Order, error) {
	order, err := gorm.G[model.Order](r.db).Where(&model.Order{ID: id}).First(context.Background())
	if err != nil {
		return nil, err
	}
	return &order, nil
}
