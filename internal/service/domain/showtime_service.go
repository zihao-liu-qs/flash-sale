package domain

import (
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/qs-lzh/flash-sale/internal/model"
	"github.com/qs-lzh/flash-sale/internal/repository"
	"github.com/qs-lzh/flash-sale/internal/service"
)

type ShowtimeService interface {
	CreateShowtime(movieID uint, startTime time.Time, totalTickets int) error
	GetShowtimeByID(showtimeID uint) (*model.Showtime, error)
	GetShowtimesByMovieID(movieID uint) ([]model.Showtime, error)
	GetShowtimesByMovieIDTx(tx *gorm.DB, movieID uint) ([]model.Showtime, error)
	GetAllShowtimes() ([]model.Showtime, error)
}

type showtimeService struct {
	db   *gorm.DB
	repo repository.ShowtimeRepo
}

var _ ShowtimeService = (*showtimeService)(nil)

func NewShowtimeService(db *gorm.DB, showtimeRepo repository.ShowtimeRepo) *showtimeService {
	return &showtimeService{
		db:   db,
		repo: showtimeRepo,
	}
}

func (s *showtimeService) CreateShowtime(movieID uint, startTime time.Time, totalTickets int) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		showtime := &model.Showtime{
			MovieID:      uint(movieID),
			StartAt:      startTime,
			TotalTickets: totalTickets,
		}
		return s.repo.WithTx(tx).Create(showtime)
	})
}

func (s *showtimeService) GetShowtimeByID(showtimeID uint) (*model.Showtime, error) {
	showtime, err := s.repo.GetByID(uint(showtimeID))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, service.ErrNotFound
		}
		return nil, err
	}
	return showtime, nil
}

func (s *showtimeService) GetShowtimesByMovieID(movieID uint) ([]model.Showtime, error) {
	return s.GetShowtimesByMovieIDTx(s.db, movieID)
}
func (s *showtimeService) GetShowtimesByMovieIDTx(tx *gorm.DB, movieID uint) ([]model.Showtime, error) {
	return s.repo.WithTx(tx).GetByMovieID(movieID)
}

func (s *showtimeService) GetAllShowtimes() ([]model.Showtime, error) {
	return s.repo.ListAll()
}
