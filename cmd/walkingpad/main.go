package main

import (
	"log/slog"
	"time"

	"github.com/getlantern/systray"
	"github.com/tim-oster/walkingpad/internal"
	"github.com/tim-oster/walkingpad/internal/app"
	"github.com/tim-oster/walkingpad/internal/walkingpads"
	"tinygo.org/x/bluetooth"
)

func main() {
	cfg, err := internal.LoadConfigFromHome()
	if err != nil {
		slog.Info("failed to load config - using defaults", "err", err)
		cfg = internal.NewDefaultConfig()
	}

	webhookThreshold := 5 * time.Minute
	if cfg.WebhookThresholdMin != nil {
		webhookThreshold = time.Duration(*cfg.WebhookThresholdMin*60.0) * time.Second
	}

	app := &app.App{
		Adapter:          bluetooth.DefaultAdapter,
		PreferredDevice:  cfg.PreferredDevice,
		TargetSpeed:      cfg.TargetSpeed,
		WebhookURL:       cfg.WebhookURL,
		WebhookThreshold: webhookThreshold,
		DiscoverFns: []internal.WalkingpadDiscovererFn{
			walkingpads.KingsmithDiscoverFn,
		},
	}
	systray.Run(app.Init, app.Close)
}
