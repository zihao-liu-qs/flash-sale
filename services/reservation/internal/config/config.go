package config

import (
	"os"

	"github.com/qs-lzh/flash-sale/pkg/config"
)

type Config struct {
	DatabaseDSN string
	Addr        string
	CacheURL    string
	MQURL       string
}

func LoadConfig() (*Config, error) {
	if err := config.LoadEnv(); err != nil {
		return nil, err
	}
	return &Config{
		DatabaseDSN: os.Getenv("DATABASE_DSN"),
		Addr:        os.Getenv("ADDR"),
		CacheURL:    os.Getenv("CACHE_URL"),
		MQURL:       os.Getenv("RABBIT_MQ_URL"),
	}, nil
}
