package outbox

import (
	"context"
	"encoding/json"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"gorm.io/gorm"

	"github.com/qs-lzh/flash-sale/pkg/logger"
	pkgmq "github.com/qs-lzh/flash-sale/pkg/mq"
	"github.com/qs-lzh/flash-sale/services/reservation/internal/model"
)

type Store struct {
	db *gorm.DB
}

func NewStore(db *gorm.DB) *Store {
	return &Store{db: db}
}

func (s *Store) Insert(queueName string, message any) error {
	body, _ := json.Marshal(message)
	return s.db.Create(&model.Outbox{
		QueueName: queueName,
		Body:      string(body),
	}).Error
}

type Dispatcher struct {
	db     *gorm.DB
	mqConn *amqp.Connection
}

func NewDispatcher(db *gorm.DB, mqConn *amqp.Connection) *Dispatcher {
	return &Dispatcher{db: db, mqConn: mqConn}
}

func (d *Dispatcher) Start() {
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		for range ticker.C {
			d.process()
		}
	}()
}

func (d *Dispatcher) process() {
	var records []model.Outbox
	if err := d.db.Where("published_at IS NULL").Order("id").Limit(20).Find(&records).Error; err != nil {
		logger.Log.Errorf("Outbox dispatch query failed: %v", err)
		return
	}

	for _, record := range records {
		ch, err := pkgmq.NewChannel(d.mqConn)
		if err != nil {
			logger.Log.Errorf("Outbox dispatch channel failed: %v", err)
			continue
		}

		if err := ch.PublishWithContext(context.Background(), "", record.QueueName, false, false,
			amqp.Publishing{
				ContentType:  "application/json",
				DeliveryMode: amqp.Persistent,
				Body:         []byte(record.Body),
			}); err != nil {
			ch.Close()
			logger.Log.Errorf("Outbox publish failed (id=%d): %v", record.ID, err)
			continue
		}
		ch.Close()

		now := time.Now()
		d.db.Model(&record).Update("published_at", &now)
	}
}
