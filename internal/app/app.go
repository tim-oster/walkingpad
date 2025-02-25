package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/getlantern/systray"
	"github.com/tim-oster/walkingpad/internal"
	"tinygo.org/x/bluetooth"
)

type connectionState byte

const (
	connectionStateDisconnected connectionState = iota
	connectionStateScanning
	connectionStateConnecting
	connectionStateConnected
	connectionStateReady
)

const (
	GitHubURL = "https://github.com/tim-oster/walkingpad"
)

type App struct {
	Adapter          *bluetooth.Adapter
	PreferredDevice  string
	TargetSpeed      float64
	WebhookURL       *string
	WebhookThreshold time.Duration

	DiscoverFns []internal.WalkingpadDiscovererFn

	pad   *Walkingpad
	state state

	mStartPause *systray.MenuItem
	mStop       *systray.MenuItem
	mSpeedItems []speedItem
}

type state struct {
	connState connectionState
	started   bool

	status internal.UpdateStats

	startedAt time.Time

	timeAccum, timeAccumTotal   time.Duration
	stepsAccum, stepsAccumTotal int
	kmAccum, kmAccumTotal       float64
}

type speedItem struct {
	speed float64
	item  *systray.MenuItem
}

func (app *App) Init() {
	app.setupUI()
	app.updateUI()

	err := app.Adapter.Enable()
	if err != nil {
		panic(fmt.Sprintf("init bluetooth: %s", err))
	}
	app.Adapter.SetConnectHandler(app.onConnectionStateChange)

	// main loop - blocking
	for {
		if app.state.connState == connectionStateDisconnected {
			err := app.attemptToConnect()
			if err != nil {
				slog.Error("attemptToConnect", "err", err)
			}
			if app.state.connState == connectionStateDisconnected {
				// if still not connected, wait a bit before trying again
				time.Sleep(5 * time.Second)
				continue
			}
		}

		if app.state.connState == connectionStateConnected && !app.pad.GetStats().Timestamp.IsZero() {
			app.state.connState = connectionStateReady
		}

		if app.state.connState == connectionStateReady {
			lastStatus := app.state.status
			app.state.status = app.pad.GetStats()

			// sync external changes
			tempoDiff := app.state.status.Speed - lastStatus.Speed
			if !app.state.started && tempoDiff > 0 {
				app.onBeltStart()
			}
			if app.state.started && tempoDiff < 0 && app.state.status.Speed == 0 {
				app.onBeltStop()
			}

			// increment difference to accumulate until stopped
			if app.state.started {
				timeDiff := app.state.status.Time - lastStatus.Time
				stepsDiff := app.state.status.Steps - lastStatus.Steps
				kmDiff := app.state.status.WalkedKM - lastStatus.WalkedKM
				if timeDiff >= 0 && stepsDiff >= 0 && kmDiff >= 0 {
					app.state.timeAccum += timeDiff
					app.state.stepsAccum += stepsDiff
					app.state.kmAccum += kmDiff
					app.state.timeAccumTotal += timeDiff
					app.state.stepsAccumTotal += stepsDiff
					app.state.kmAccumTotal += kmDiff
				}
			}
		} else {
			app.state.started = false
			app.state.status = internal.UpdateStats{}
		}

		app.updateUI()
		time.Sleep(500 * time.Millisecond)
	}
}

func (app *App) setupUI() {
	systray.SetTitle("WP: connecting")

	app.mStartPause = systray.AddMenuItem("Start", "")
	app.mStop = systray.AddMenuItem("Stop", "")

	app.mStartPause.ClickedCh = make(chan struct{})
	app.mStop.ClickedCh = make(chan struct{})

	go func() {
		for {
			select {
			case <-app.mStartPause.ClickedCh:
				if !app.state.started {
					app.onBeltStart()
					app.pad.Send(&internal.CmdStart{Speed: app.TargetSpeed})
				} else {
					app.pad.Send(&internal.CmdStop{})
					app.onBeltStop()
				}
			case <-app.mStop.ClickedCh:
				if app.state.started {
					app.pad.Send(&internal.CmdStop{})
					app.onBeltStop()
				}

				app.state.startedAt = time.Time{}
				app.state.timeAccum = 0
				app.state.stepsAccum = 0
				app.state.kmAccum = 0
				app.state.timeAccumTotal = 0
				app.state.stepsAccumTotal = 0
				app.state.kmAccumTotal = 0
			}

			app.updateUI()
		}
	}()

	selectedSpeed := 2.5
	mSpeed := systray.AddMenuItem("Speed", "")
	var (
		speedClickCh []chan struct{}
	)
	for speed := 0.5; speed <= 6.0; speed += 0.5 {
		item := mSpeed.AddSubMenuItem(fmt.Sprintf("%.1f km/h", speed), "")
		if speed == selectedSpeed {
			item.Check()
		}
		item.ClickedCh = make(chan struct{})

		app.mSpeedItems = append(app.mSpeedItems, speedItem{speed: speed, item: item})
		speedClickCh = append(speedClickCh, item.ClickedCh)
	}
	go func() {
		var cases []reflect.SelectCase
		for _, ch := range speedClickCh {
			cases = append(cases, reflect.SelectCase{
				Dir:  reflect.SelectRecv,
				Chan: reflect.ValueOf(ch),
			})
		}
		for {
			chosen, _, ok := reflect.Select(cases)
			if ok {
				selectedSpeed = app.mSpeedItems[chosen].speed
				app.TargetSpeed = selectedSpeed
				app.updateUI()

				if app.state.connState == connectionStateReady && app.state.started {
					app.pad.Send(&internal.CmdChangeSpeed{Speed: selectedSpeed})
				}
			}
		}
	}()

	mGitHub := systray.AddMenuItem("GitHub", "")
	mGitHub.ClickedCh = make(chan struct{})
	go func() {
		for {
			<-mGitHub.ClickedCh
			err := openURL(GitHubURL)
			if err != nil {
				slog.Error("openURL", "err", err)
			}
		}
	}()

	mQuit := systray.AddMenuItem("Quit", "")
	mQuit.ClickedCh = make(chan struct{})
	go func() {
		<-mQuit.ClickedCh
		systray.Quit()
	}()
}

func (app *App) updateUI() {
	switch app.state.connState {
	case connectionStateDisconnected:
		systray.SetTitle("WP: disconnected")
	case connectionStateScanning:
		systray.SetTitle("WP: scanning")
	case connectionStateConnecting:
		systray.SetTitle("WP: connecting")
	case connectionStateConnected:
		systray.SetTitle("WP: connected")
	case connectionStateReady:
		systray.SetTitle(fmt.Sprintf(
			"WP: %s - %.2f km (~%d steps) @ [%.1f km/h]",
			app.state.timeAccumTotal,
			app.state.kmAccumTotal,
			app.state.stepsAccumTotal,
			app.state.status.Speed,
		))
	}

	if !app.state.started {
		app.mStartPause.SetTitle("Start")
		app.mStop.Disable()
	} else {
		app.mStartPause.SetTitle("Pause")
		app.mStop.Enable()
	}
	if !app.state.started && app.state.timeAccumTotal != 0 {
		app.mStop.Enable()
	}

	if app.state.connState != connectionStateReady {
		app.mStartPause.Disable()
	} else {
		app.mStartPause.Enable()
	}

	for _, si := range app.mSpeedItems {
		if si.speed == app.TargetSpeed {
			si.item.Check()
			continue
		}
		si.item.Uncheck()
	}
}

func (app *App) onConnectionStateChange(device bluetooth.Device, connected bool) {
	if app.pad != nil && device.Address.String() == app.pad.addr && !connected {
		app.disconnectConnectedPad()
	}
}

func (app *App) disconnectConnectedPad() {
	if app.pad != nil {
		slog.Info("disconnect walking pad", "device", app.pad.addr)

		app.pad.Disconnect()
		app.pad = nil
	}

	app.state.connState = connectionStateDisconnected
	app.updateUI()
}

func (app *App) attemptToConnect() error {
	if app.pad != nil {
		app.disconnectConnectedPad()
	}

	// ensure that state is reset in case of errors
	defer func() {
		if app.state.connState != connectionStateConnected {
			app.state.connState = connectionStateDisconnected
		}
	}()

	slog.Info("start scan")
	app.state.connState = connectionStateScanning
	app.updateUI()

	var preferredDevice *string
	if app.PreferredDevice != "" {
		preferredDevice = &app.PreferredDevice
	}
	devices, err := internal.DiscoverWalkingpadCandidates(app.Adapter, 5*time.Second, app.DiscoverFns, preferredDevice)
	if err != nil {
		return fmt.Errorf("find walking pad candidates: %w", err)
	}

	for _, device := range devices {
		slog.Info("found walking pad", "device", device.Device.Address.String())
	}

	if len(devices) == 0 {
		slog.Info("no walking pad found")
		app.state.connState = connectionStateDisconnected
		app.updateUI()
		return nil
	}

	slog.Info("connecting walking pad", "device", devices[0].Device.Address.String())
	app.state.connState = connectionStateConnecting
	app.updateUI()

	app.pad, err = NewWalkingpadFromCandidate(app.Adapter, devices[0])
	if err != nil {
		return fmt.Errorf("connect walking pad: %w", err)
	}

	slog.Info("connected to walking pad", "device", app.pad.addr)
	app.state.connState = connectionStateConnected
	app.updateUI()

	return nil
}

func (app *App) onBeltStart() {
	app.state.started = true
	app.state.startedAt = time.Now()
}

func (app *App) onBeltStop() {
	app.state.started = false

	sentWebhook, err := app.sendWebhook()
	if err != nil {
		slog.Error("sendWebhook", "err", err)
	}

	if sentWebhook {
		// only reset if the webhook was sent - otherwise keep the data for the next attempt
		app.state.startedAt = time.Time{}
		app.state.timeAccum = 0
		app.state.stepsAccum = 0
		app.state.kmAccum = 0
	}
}

func (app *App) sendWebhook() (sent bool, err error) {
	if app.WebhookURL == nil {
		return false, nil
	}
	if time.Since(app.state.startedAt) < app.WebhookThreshold {
		slog.Info("skip webhook: session length too short")
		return false, nil
	}

	reqURL := *app.WebhookURL
	reqURL = strings.NewReplacer(
		"{start_ts}", url.QueryEscape(app.state.startedAt.Format(time.RFC3339)),
		"{duration_min}", url.QueryEscape(fmt.Sprintf("%.2f", app.state.timeAccum.Minutes())),
		"{steps}", url.QueryEscape(fmt.Sprintf("%d", app.state.stepsAccum)),
		"{distance_km}", url.QueryEscape(fmt.Sprintf("%.2f", app.state.kmAccum)),
	).Replace(reqURL)

	var statusCode int
	defer func() {
		var errStr string
		if err != nil {
			errStr = err.Error()
		}

		line := webhookLogLine{
			Timestamp:   time.Now(),
			URL:         reqURL,
			Status:      statusCode,
			Err:         errStr,
			StartAt:     app.state.startedAt,
			DurationMin: app.state.timeAccum.Minutes(),
			Steps:       app.state.stepsAccum,
			DistanceKm:  app.state.kmAccum,
		}
		err = logWebhook(line)
		if err != nil {
			slog.Error("logWebhook", "err", err)
		}
	}()

	slog.Info("send webhook", "url", reqURL)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return false, fmt.Errorf("create request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("send request: %w", err)
	}
	statusCode = resp.StatusCode

	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return true, nil
}

type webhookLogLine struct {
	Timestamp   time.Time `json:"timestamp"`
	URL         string    `json:"url"`
	Status      int       `json:"status"`
	Err         string    `json:"err,omitempty"`
	StartAt     time.Time `json:"start_ts"`
	DurationMin float64   `json:"duration_min"`
	Steps       int       `json:"steps"`
	DistanceKm  float64   `json:"distance_km"`
}

func logWebhook(line webhookLogLine) error {
	logLine, err := json.Marshal(line)
	if err != nil {
		return fmt.Errorf("failed to marshal log line: %w", err)
	}

	configDir, err := os.UserConfigDir()
	if err != nil {
		return fmt.Errorf("failed to get user config dir: %w", err)
	}

	configPath := filepath.Join(configDir, "walkingpad_webhooks.jsonl")

	logFile, err := os.OpenFile(configPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}
	defer func() { _ = logFile.Close() }()

	_, err = logFile.WriteString(string(logLine) + "\n")
	if err != nil {
		return fmt.Errorf("failed to write to log file: %w", err)
	}

	return nil
}

func (app *App) Close() {
	app.disconnectConnectedPad()
}
