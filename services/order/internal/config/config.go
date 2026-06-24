package config

import (
	"os"

	"github.com/qs-lzh/flash-sale/pkg/config"
)

type Config struct {
	DatabaseDSN string
	MQURL       string
}

func LoadConfig() (*Config, error) {
	if err := config.LoadEnv(); err != nil {
		return nil, err
	}
	return &Config{
		DatabaseDSN: os.Getenv("DATABASE_DSN"),
		MQURL:       os.Getenv("RABBIT_MQ_URL"),
	}, nil
}
