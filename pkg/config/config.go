package config

import (
	"os"
	"path/filepath"
	"strings"

	consulapi "github.com/hashicorp/consul/api"
	"github.com/joho/godotenv"
)

// LoadEnv walks up from the working directory to find and load a .env file.
func LoadEnv() error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	for {
		envPath := filepath.Join(dir, ".env")
		if _, err := os.Stat(envPath); err == nil {
			return godotenv.Load(envPath)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return nil
}

// LoadFromConsul reads config from Consul KV and sets env vars.
// Overrides any existing env vars with the same key.
func LoadFromConsul(consulAddr, prefix string) error {
	client, err := consulapi.NewClient(&consulapi.Config{Address: consulAddr})
	if err != nil {
		return err
	}
	kv := client.KV()
	pairs, _, err := kv.List(prefix, nil)
	if err != nil {
		return err
	}
	for _, p := range pairs {
		key := strings.TrimPrefix(p.Key, prefix)
		key = strings.ReplaceAll(key, "/", "_")
		key = strings.ToUpper(key)
		if err := os.Setenv(key, string(p.Value)); err != nil {
			return err
		}
	}
	return nil
}
