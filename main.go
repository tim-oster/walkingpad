package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/getlantern/systray"
	"tinygo.org/x/bluetooth"
)

func main() {
	cfg, err := tryLoadConfig()
	if err != nil {
		slog.Error("failed to load config", "err", err)
		cfg = &Config{
			PreferredDevice: "",
			TargetSpeed:     2.5,
		}
	}

	app := &App{
		Adapter:         bluetooth.DefaultAdapter,
		PreferredDevice: cfg.PreferredDevice,
		TargetSpeed:     cfg.TargetSpeed,
	}
	systray.Run(app.Init, app.Close)
}

type Config struct {
	PreferredDevice string  `json:"preferredDevice"`
	TargetSpeed     float64 `json:"targetSpeed"`
}

func tryLoadConfig() (*Config, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get user config dir: %w", err)
	}

	configPath := filepath.Join(configDir, "walkingpad.json")
	slog.Info("configPath", "path", configPath)

	configFile, err := os.OpenFile(configPath, os.O_RDWR|os.O_CREATE, 0644)
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
