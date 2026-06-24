package repository

import (
	"gorm.io/gorm"

	"github.com/qs-lzh/flash-sale/services/reservation/internal/model"
)

type Repo struct {
	db *gorm.DB
}

func New(db *gorm.DB) *Repo {
	return &Repo{db: db}
}

func (r *Repo) CreateReservation(id, showtimeID, userID uint, state model.SagaState) error {
	return r.db.Create(&model.Reservation{
		ID:         id,
		ShowtimeID: showtimeID,
		UserID:     userID,
		State:      state,
	}).Error
}

func (r *Repo) UpdateState(id uint, state model.SagaState) error {
	return r.db.Model(&model.Reservation{}).Where("id = ?", id).Update("state", state).Error
}

func (r *Repo) GetByID(id uint) (*model.Reservation, error) {
	var res model.Reservation
	if err := r.db.First(&res, id).Error; err != nil {
		return nil, err
	}
	return &res, nil
}
