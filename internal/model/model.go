package model

import (
	"time"
)

type User struct {
	ID             uint     `gorm:"primaryKey"`
	Name           string   `gorm:"size:64;not null;uniqueIndex"`
	HashedPassword string   `gorm:"not null"`
	Role           UserRole `gorm:"type:varchar(16);not null"`
}

type UserRole string

const (
	RoleUser  UserRole = "user"
	RoleAdmin UserRole = "admin"
)

type Movie struct {
	ID          uint   `gorm:"primaryKey"`
	Title       string `gorm:"size:100;not null;uniqueIndex"`
	Description string `gorm:"type:text"`
}

type Showtime struct {
	ID      uint      `gorm:"primaryKey"`
	MovieID uint      `gorm:"not null;index"`
	StartAt time.Time `gorm:"not null"`
	// 场次总票数。库存的 DB 真相：启动时据此初始化 Redis 库存，
	// 替代原先的硬编码 100
	TotalTickets int `gorm:"not null"`
}

type Order struct {
	ID         uint `gorm:"primaryKey;autoIncrement:false"`
	ShowtimeID uint `gorm:"not null;index"`
	UserID     uint `gorm:"not null;index"`
}
