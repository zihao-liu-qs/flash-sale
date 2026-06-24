package config

import (
	"os"

	"github.com/qs-lzh/flash-sale/pkg/config"
)

type Config struct {
	MQURL string
}

func LoadConfig() (*Config, error) {
	if err := config.LoadEnv(); err != nil {
		return nil, err
	}
	return &Config{
		MQURL: os.Getenv("RABBIT_MQ_URL"),
	}, nil
}
