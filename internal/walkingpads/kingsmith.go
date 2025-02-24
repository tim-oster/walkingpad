package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/tim-oster/walkingpad/internal"
	"tinygo.org/x/bluetooth"
)

var kingsmithUUIDs = []bluetooth.UUID{
	mustUUID("00001800-0000-1000-8000-00805f9b34fb"),
	mustUUID("0000180a-0000-1000-8000-00805f9b34fb"),
	mustUUID("00010203-0405-0607-0809-0a0b0c0d1912"),
	mustUUID("0000fe00-0000-1000-8000-00805f9b34fb"),
	mustUUID("00002902-0000-1000-8000-00805f9b34fb"),
	mustUUID("00010203-0405-0607-0809-0a0b0c0d1912"),
	mustUUID("00002901-0000-1000-8000-00805f9b34fb"),
	mustUUID("00002a00-0000-1000-8000-00805f9b34fb"),
	mustUUID("00002a01-0000-1000-8000-00805f9b34fb"),
	mustUUID("00002a04-0000-1000-8000-00805f9b34fb"),
	mustUUID("00002a25-0000-1000-8000-00805f9b34fb"),
	mustUUID("00002a26-0000-1000-8000-00805f9b34fb"),
	mustUUID("00002a28-0000-1000-8000-00805f9b34fb"),
	mustUUID("00002a24-0000-1000-8000-00805f9b34fb"),
	mustUUID("00002a29-0000-1000-8000-00805f9b34fb"),
	mustUUID("0000fe01-0000-1000-8000-00805f9b34fb"),
	mustUUID("0000fe02-0000-1000-8000-00805f9b34fb"),
	mustUUID("00010203-0405-0607-0809-0a0b0c0d2b12"),
}

func mustUUID(uuid string) bluetooth.UUID {
	u, err := bluetooth.ParseUUID(uuid)
	if err != nil {
		panic(err)
	}
	return u
}

func sleepCtx(ctx context.Context, timeout time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(timeout):
	}
}

func init() {
	internal.WalkingpadDiscoverer = append(internal.WalkingpadDiscoverer, func(adapter *bluetooth.Adapter, device bluetooth.ScanResult) (internal.WalkingpadCandidate, bool) {
		for _, uuid := range kingsmithUUIDs {
			if device.HasServiceUUID(uuid) {
				return internal.WalkingpadCandidate{
					Device: device,
					Connect: func(adapter *bluetooth.Adapter, candidate internal.WalkingpadCandidate) (<-chan internal.WalkingpadUpdate, chan<- internal.WalkingpadCommand, error) {
						device, err := adapter.Connect(candidate.Device.Address, bluetooth.ConnectionParams{})
						if err != nil {
							return nil, nil, fmt.Errorf("connect: %w", err)
						}

						services, err := device.DiscoverServices(nil)
						if err != nil {
							return nil, nil, fmt.Errorf("discover services: %w", err)
						}

						var (
							rxFound, txFound bool
							rx               bluetooth.DeviceCharacteristic
							tx               bluetooth.DeviceCharacteristic
						)
						for _, service := range services {
							characteristics, err := service.DiscoverCharacteristics(nil)
							if err != nil {
								return nil, nil, fmt.Errorf("discover characteristics: %w", err)
							}

							for _, ch := range characteristics {
								if strings.HasPrefix(ch.UUID().String(), "0000fe01") {
									rx = ch
									rxFound = true
								}
								if strings.HasPrefix(ch.UUID().String(), "0000fe02") {
									tx = ch
									txFound = true
								}
							}
						}

						if !rxFound || !txFound {
							return nil, nil, fmt.Errorf("missing characteristics")
						}

						pad := &KingsmithPad{
							device: device,
							rx:     rx,
							tx:     tx,
							// allow channels to function as queues to avoid UI blocking in case processing is slow
							updateChan: make(chan internal.WalkingpadUpdate, 50),
							queue:      make(chan kingsmithCommand, 50),
						}
						cmdChan := make(chan internal.WalkingpadCommand, 50)

						_ = pad.rx.EnableNotifications(pad.onBufferReceive)

						var ctx context.Context
						ctx, pad.cancel = context.WithCancel(context.Background())

						pad.wg.Add(3)
						go pad.processCmds(cmdChan)
						go pad.writeLoop(ctx)
						go pad.askStatsLoop(ctx)

						return pad.updateChan, cmdChan, nil
					},
				}, true
			}
		}
		return internal.WalkingpadCandidate{}, false
	})
}

type KingsmithPad struct {
	device bluetooth.Device
	rx     bluetooth.DeviceCharacteristic
	tx     bluetooth.DeviceCharacteristic

	updateChan chan internal.WalkingpadUpdate

	wg      sync.WaitGroup
	cancel  context.CancelFunc
	stopped bool

	queue          chan kingsmithCommand
	LastStatus     KingsmithPadStatus
	LastStatusTime time.Time
}

type kingsmithCommand struct {
	timeout time.Duration
	buffer  []byte
}

func (pad *KingsmithPad) Disconnect() {
	if pad.stopped {
		return
	}
	pad.stopped = true

	close(pad.updateChan)
	close(pad.queue)
	pad.cancel()
	pad.wg.Wait()
	_ = pad.device.Disconnect()
}

func (pad *KingsmithPad) pushCmd(cmd []byte, timeout time.Duration) {
	pad.fixCrc(cmd)
	pad.queue <- kingsmithCommand{timeout: timeout, buffer: cmd}
}

func (pad *KingsmithPad) ChangeMode(mode KingsmithPadMode) {
	pad.pushCmd([]byte{247, 162, 2, byte(mode), 0xFF, 253}, 0)
}

func (pad *KingsmithPad) StartBelt() {
	pad.pushCmd([]byte{247, 162, 4, 1, 0xFF, 253}, 0)
}

func (pad *KingsmithPad) StopBelt() {
	pad.ChangeSpeed(0.0)
}

func (pad *KingsmithPad) ChangeSpeed(speed float64) {
	if speed < 0 || speed > 6 {
		panic("invalid speed")
	}
	cnv := byte(speed * 10.0)
	pad.pushCmd([]byte{247, 162, 1, cnv, 0xFF, 253}, 0)
}

func (pad *KingsmithPad) AskStats() {
	pad.pushCmd([]byte{247, 162, 0, 0, 162, 253}, 0)
}

func (pad *KingsmithPad) WaitCmd(timeout time.Duration) {
	pad.pushCmd(nil, timeout)
}

func (pad *KingsmithPad) onBufferReceive(buf []byte) {
	if len(buf) < 2 {
		return
	}

	if buf[0] == 248 && buf[1] == 162 {
		status := readKingsmithStatusBuffer(buf[2:])
		pad.LastStatus = status
		pad.LastStatusTime = time.Now()

		msg := internal.UpadteStats{
			Speed:    status.Speed,
			Time:     status.Time,
			WalkedKM: status.WalkedKM,
			Steps:    status.Steps,
		}
		select {
		case pad.updateChan <- msg:
		default:
		}

		return
	}
}

func (pad *KingsmithPad) processCmds(cmdChan <-chan internal.WalkingpadCommand) {
	defer pad.wg.Done()

	for cmd := range cmdChan {
		switch cmd := cmd.(type) {
		case *internal.CmdStart:
			if pad.LastStatus.Mode == KingsmithPadModeStandby {
				// the pad does not process messages in standby mode, which it can enter automatically after inactivity
				pad.ChangeMode(KingsmithPadModeManual)
			}
			pad.StartBelt()
			pad.WaitCmd(2500 * time.Millisecond) // delay so that the change beep comes after the three starting beeps
			pad.ChangeSpeed(cmd.Speed)

		case *internal.CmdStop:
			pad.StopBelt()

		case *internal.CmdChangeSpeed:
			pad.ChangeSpeed(cmd.Speed)

		default:
			slog.Error("invalid cmd type", slog.String("type", fmt.Sprintf("%T", cmd)))
		}
	}

	// if the app closes the cmd channel, it intends to disconnec the device
	pad.Disconnect()
}

func (pad *KingsmithPad) writeLoop(ctx context.Context) {
	defer pad.wg.Done()

	for cmd := range pad.queue {
		if cmd.timeout != 0 {
			sleepCtx(ctx, cmd.timeout)
		}
		if cmd.buffer != nil {
			_, err := pad.tx.WriteWithoutResponse(cmd.buffer)
			if err != nil {
				slog.Error("error writing to bluetooth device", "err", err)
			}

			// sleep to avoid overwhelming the pad
			sleepCtx(ctx, 700*time.Millisecond)
		}
	}
}

func (pad *KingsmithPad) askStatsLoop(ctx context.Context) {
	defer pad.wg.Done()

	ticket := time.NewTicker(3 * time.Second)
	defer ticket.Stop()

	pad.AskStats()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticket.C:
			pad.AskStats()
		}
	}
}

func (*KingsmithPad) fixCrc(cmd []byte) {
	if len(cmd) < 2 {
		return
	}
	var sum byte
	for i := 1; i < len(cmd)-2; i++ {
		sum += cmd[i] // overflow intended
	}
	cmd[len(cmd)-2] = sum
}

type KingsmithPadMode byte

const (
	KingsmithPadModeStandby KingsmithPadMode = 2
	KingsmithPadModeManual  KingsmithPadMode = 1
	KingsmithPadModeAuto    KingsmithPadMode = 0
)

type KingsmithPadStatus struct {
	Speed    float64
	Mode     KingsmithPadMode
	Time     time.Duration
	WalkedKM float64
	Steps    int
}

func readKingsmithStatusBuffer(buf []byte) KingsmithPadStatus {
	timeS := int(buf[3])<<16 | int(buf[4])<<8 | int(buf[5])
	dist := int(buf[6])<<16 | int(buf[7])<<8 | int(buf[8])
	return KingsmithPadStatus{
		Speed:    float64(buf[1]) / 10.0,
		Mode:     KingsmithPadMode(buf[2]),
		Time:     time.Duration(timeS) * time.Second,
		WalkedKM: float64(dist) / 100.0,
		Steps:    int(buf[9])<<16 | int(buf[10])<<8 | int(buf[11]),
	}
}
