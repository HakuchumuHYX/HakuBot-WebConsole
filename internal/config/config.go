package config

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	ListenAddress string
	DatabasePath  string
	SpoolPath     string
	PublicOrigin  string
	RetentionDays int
	TokenSecret   []byte
}

type fileConfig struct {
	ListenAddress string `json:"listen"`
	DatabasePath  string `json:"database_path"`
	SpoolPath     string `json:"spool_path"`
	PublicOrigin  string `json:"public_origin"`
	RetentionDays *int   `json:"retention_days"`
	TokenSecret   string `json:"token_secret"`
}

func Load() (Config, error) {
	fileCfg, err := loadFileConfig()
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		ListenAddress: valueWithOverride(
			"WEBCONSOLE_LISTEN",
			fileCfg.ListenAddress,
			"127.0.0.1:54322",
		),
		DatabasePath: valueWithOverride(
			"WEBCONSOLE_DB_PATH",
			fileCfg.DatabasePath,
			"/var/lib/hakubot-webconsole/webconsole.db",
		),
		SpoolPath: valueWithOverride(
			"WEBCONSOLE_SPOOL_PATH",
			fileCfg.SpoolPath,
			"/var/lib/hakubot-webconsole/spool",
		),
		PublicOrigin: strings.TrimRight(
			valueWithOverride(
				"WEBCONSOLE_PUBLIC_ORIGIN",
				fileCfg.PublicOrigin,
				"",
			),
			"/",
		),
	}

	if err := validateLoopbackAddress(cfg.ListenAddress); err != nil {
		return Config{}, err
	}
	if !filepath.IsAbs(cfg.DatabasePath) {
		return Config{}, errors.New("WEBCONSOLE_DB_PATH must be absolute")
	}
	if !filepath.IsAbs(cfg.SpoolPath) {
		return Config{}, errors.New("WEBCONSOLE_SPOOL_PATH must be absolute")
	}

	retentionDefault := 90
	if fileCfg.RetentionDays != nil {
		retentionDefault = *fileCfg.RetentionDays
	}
	retentionDays, err := parseBoundedInt(
		"WEBCONSOLE_RETENTION_DAYS",
		retentionDefault,
		0,
		3650,
	)
	if err != nil {
		return Config{}, err
	}
	cfg.RetentionDays = retentionDays

	secretRaw := strings.TrimSpace(os.Getenv("WEBCONSOLE_TOKEN_SECRET"))
	if secretRaw == "" {
		secretRaw = strings.TrimSpace(fileCfg.TokenSecret)
	}
	secret, err := loadSecret(secretRaw)
	if err != nil {
		return Config{}, err
	}
	cfg.TokenSecret = secret
	return cfg, nil
}

func loadFileConfig() (fileConfig, error) {
	path := strings.TrimSpace(os.Getenv("WEBCONSOLE_CONFIG"))
	required := path != ""
	if path == "" {
		path = "config.json"
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) && !required {
		return fileConfig{}, nil
	}
	if err != nil {
		return fileConfig{}, fmt.Errorf("open WebConsole config %q: %w", path, err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var cfg fileConfig
	if err := decoder.Decode(&cfg); err != nil {
		return fileConfig{}, fmt.Errorf(
			"decode WebConsole config %q: %w",
			path,
			err,
		)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return fileConfig{}, fmt.Errorf(
			"decode WebConsole config %q: %w",
			path,
			err,
		)
	}
	return cfg, nil
}

func valueWithOverride(name, configured, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	if value := strings.TrimSpace(configured); value != "" {
		return value
	}
	return fallback
}

func parseBoundedInt(name string, fallback, minimum, maximum int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		if fallback < minimum || fallback > maximum {
			return 0, fmt.Errorf(
				"%s must be an integer between %d and %d",
				name,
				minimum,
				maximum,
			)
		}
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minimum || value > maximum {
		return 0, fmt.Errorf(
			"%s must be an integer between %d and %d",
			name,
			minimum,
			maximum,
		)
	}
	return value, nil
}

func validateLoopbackAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid WEBCONSOLE_LISTEN: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New(
			"WEBCONSOLE_LISTEN must use a literal loopback IP address",
		)
	}
	return nil
}

func loadSecret(raw string) ([]byte, error) {
	if raw != "" {
		secret, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil || len(secret) < 32 {
			return nil, errors.New(
				"WEBCONSOLE_TOKEN_SECRET must be raw URL-safe base64 " +
					"encoding at least 32 bytes",
			)
		}
		return secret, nil
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("generate ephemeral token secret: %w", err)
	}
	return secret, nil
}
