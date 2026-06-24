package model

import (
	"time"

	"gorm.io/gorm"
)

type Showtime struct {
	ID      uint      `gorm:"primaryKey"`
	MovieID uint      `gorm:"not null;index"`
	StartAt time.Time `gorm:"not null"`
}

// SagaState tracks the saga status for each reservation.
type SagaState string

const (
	SagaStateReserved SagaState = "RESERVED"
	SagaStatePaid     SagaState = "PAID"
	SagaStateTimeout  SagaState = "TIMEOUT"
)

type Reservation struct {
	ID          uint           `gorm:"primaryKey"`
	ShowtimeID  uint           `gorm:"not null;index"`
	UserID      uint           `gorm:"not null;index"`
	State       SagaState      `gorm:"not null;default:RESERVED"`
	CreatedAt   time.Time      `gorm:"autoCreateTime"`
	UpdatedAt   time.Time      `gorm:"autoUpdateTime"`
	DeletedAt   gorm.DeletedAt `gorm:"index"`
}

// Outbox record for reliable MQ publishing.
type Outbox struct {
	ID          uint           `gorm:"primaryKey"`
	QueueName   string         `gorm:"not null"`
	Body        string         `gorm:"not null;type:text"`
	PublishedAt *time.Time     `gorm:"index"`
	CreatedAt   time.Time      `gorm:"autoCreateTime"`
	DeletedAt   gorm.DeletedAt `gorm:"index"`
}
