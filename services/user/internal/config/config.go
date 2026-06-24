package config

import (
	"os"

	"github.com/qs-lzh/flash-sale/pkg/config"
)

type Config struct {
	DatabaseDSN string
	Addr        string
}

func LoadConfig() (*Config, error) {
	if err := config.LoadEnv(); err != nil {
		return nil, err
	}
	return &Config{
		DatabaseDSN: os.Getenv("USER_DATABASE_DSN"),
		Addr:        os.Getenv("USER_ADDR"),
	}, nil
}
