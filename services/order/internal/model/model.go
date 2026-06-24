package model

type Order struct {
	ID         uint `gorm:"primaryKey;autoIncrement:false"`
	ShowtimeID uint `gorm:"not null;index"`
	UserID     uint `gorm:"not null;index"`
}
