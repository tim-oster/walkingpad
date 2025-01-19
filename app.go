package main

import (
	"fmt"
	"log/slog"
	"reflect"
	"time"

	"github.com/getlantern/systray"
	"tinygo.org/x/bluetooth"
)

const (
	GitHubURL = "https://github.com/tim-oster/walkingpad"
)

type connectionState byte

const (
	connectionStateDisconnected connectionState = iota
	connectionStateScanning
	connectionStateConnecting
	connectionStateConnected
	connectionStateReady
)

type speedItem struct {
	speed float64
	item  *systray.MenuItem
}

type App struct {
	Adapter         *bluetooth.Adapter
	PreferredDevice string
	TargetSpeed     float64

	pad   *WalkingPad
	state state

	mStartPause *systray.MenuItem
	mStop       *systray.MenuItem
	mSpeedItems []speedItem
}

type state struct {
	connState connectionState
	started   bool
	status    WalkingPadStatus

	timeAccum  time.Duration
	stepsAccum int
	kmAccum    float64
}

func (app *App) Init() {
	app.setupUI()
	app.updateUI()

	err := app.Adapter.Enable()
	if err != nil {
		panic(fmt.Sprintf("init bluetooth: %s", err))
	}
	app.Adapter.SetConnectHandler(app.onConnectionStateChange)

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

		if app.state.connState == connectionStateConnected && !app.pad.LastStatusTime.IsZero() {
			app.state.connState = connectionStateReady
		}

		if app.state.connState == connectionStateReady {
			lastStatus := app.state.status
			app.state.status = app.pad.LastStatus

			// increment difference to accumulate until stopped
			if app.state.started {
				timeDiff := app.state.status.Time - lastStatus.Time
				stepsDiff := app.state.status.Steps - lastStatus.Steps
				kmDiff := app.state.status.WalkedKM - lastStatus.WalkedKM
				if timeDiff >= 0 && stepsDiff >= 0 && kmDiff >= 0 {
					app.state.timeAccum += timeDiff
					app.state.stepsAccum += stepsDiff
					app.state.kmAccum += kmDiff
				}
			}

			// sync external changes
			tempoDiff := app.state.status.Speed - lastStatus.Speed
			if !app.state.started && tempoDiff > 0 {
				app.state.started = true
			}
			if app.state.started && tempoDiff < 0 && app.state.status.Speed == 0 {
				app.state.started = false
			}
		} else {
			app.state.started = false
			app.state.status = WalkingPadStatus{}
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
					app.state.started = true

					if app.state.status.Mode == WalkingPadModeStandby {
						app.pad.ChangeMode(WalkingPadModeManual)
					}
					app.pad.StartBelt()
					app.pad.WaitCmd(2500 * time.Millisecond)
					app.pad.ChangeSpeed(app.TargetSpeed)
				} else {
					app.state.started = false

					app.pad.StopBelt()
				}
			case <-app.mStop.ClickedCh:
				if app.state.started {
					app.state.started = false
					app.pad.StopBelt()
				}

				app.state.timeAccum = 0
				app.state.stepsAccum = 0
				app.state.kmAccum = 0
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
					app.pad.ChangeSpeed(selectedSpeed)
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
			app.state.timeAccum,
			app.state.kmAccum,
			app.state.stepsAccum,
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
	if !app.state.started && app.state.timeAccum != 0 {
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
	if app.pad != nil && device.Address == app.pad.device.Address && !connected {
		app.disconnectConnectedPad()
	}
}

func (app *App) disconnectConnectedPad() {
	if app.pad != nil {
		slog.Info("disconnect walking pad", "device", app.pad.device.Address.String())

		app.pad.Disconnect()
		app.state.connState = connectionStateDisconnected
		app.pad = nil
		app.updateUI()
	}
}

func (app *App) attemptToConnect() error {
	if app.pad != nil {
		app.disconnectConnectedPad()
	}

	slog.Info("start scan")
	app.state.connState = connectionStateScanning
	app.updateUI()

	var preferredDevice *string
	if app.PreferredDevice != "" {
		preferredDevice = &app.PreferredDevice
	}
	devices, err := FindWalkingPadCandidates(app.Adapter, 5*time.Second, preferredDevice)
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

	pad, err := devices[0].Connect(app.Adapter, bluetooth.ConnectionParams{})
	if err != nil {
		return fmt.Errorf("connect walking pad: %w", err)
	}

	slog.Info("connected to walking pad", "device", pad.device.Address.String())
	app.state.connState = connectionStateConnected
	app.pad = pad
	app.updateUI()

	return nil
}

func (app *App) Close() {
	app.disconnectConnectedPad()
}
