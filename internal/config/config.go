package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	ListenAddress string `json:"listen"`
	DatabasePath  string `json:"database_path"`
	SpoolPath     string `json:"spool_path"`
	PublicOrigin  string `json:"public_origin"`
	RetentionDays int    `json:"retention_days"`
}

// Load reads config.json from the working directory; systemd sets
// WorkingDirectory to the install root.
func Load() (Config, error) {
	file, err := os.Open("config.json")
	if err != nil {
		return Config{}, fmt.Errorf("open config.json: %w", err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode config.json: %w", err)
	}
	cfg.PublicOrigin = strings.TrimRight(cfg.PublicOrigin, "/")

	host, _, err := net.SplitHostPort(cfg.ListenAddress)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return Config{}, errors.New("listen must be a literal loopback IP address with port")
	}
	if !filepath.IsAbs(cfg.DatabasePath) || !filepath.IsAbs(cfg.SpoolPath) {
		return Config{}, errors.New("database_path and spool_path must be absolute")
	}
	if cfg.PublicOrigin == "" {
		return Config{}, errors.New("public_origin is required")
	}
	if cfg.RetentionDays < 0 || cfg.RetentionDays > 3650 {
		return Config{}, errors.New("retention_days must be between 0 and 3650")
	}
	return cfg, nil
}
