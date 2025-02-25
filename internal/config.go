package internal

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

type Config struct {
	PreferredDevice     string   `json:"preferredDevice"`
	TargetSpeed         float64  `json:"targetSpeed"`
	WebhookURL          *string  `json:"webhookURL"`
	WebhookThresholdMin *float64 `json:"webhookThresholdMin"`
}

func NewDefaultConfig() *Config {
	return &Config{
		PreferredDevice:     "",
		TargetSpeed:         2.5,
		WebhookURL:          nil,
		WebhookThresholdMin: nil,
	}
}

func LoadConfigFromHome() (*Config, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get user config dir: %w", err)
	}

	configPath := filepath.Join(configDir, "walkingpad.json")
	slog.Info("configPath", "path", configPath)

	configFile, err := os.Open(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open config file: %w", err)
	}
	defer func() { _ = configFile.Close() }()

	config := &Config{}
	err = json.NewDecoder(configFile).Decode(config)
	if err != nil {
		return nil, fmt.Errorf("failed to decode config file: %w", err)
	}

	slog.Info("loaded config", "config", config)

	return config, nil
}
