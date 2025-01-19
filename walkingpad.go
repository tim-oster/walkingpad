package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"tinygo.org/x/bluetooth"
)

var walkingPadUUIDs = []bluetooth.UUID{
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

type WalkingPadCandidate struct {
	Device bluetooth.ScanResult
}

func FindWalkingPadCandidates(adapter *bluetooth.Adapter, timeout time.Duration, targetAddr *string) ([]WalkingPadCandidate, error) {
	go func() {
		<-time.After(timeout)
		_ = adapter.StopScan()
	}()

	var (
		set     = make(map[string]struct{})
		devices []WalkingPadCandidate
	)
	err := adapter.Scan(func(adapter *bluetooth.Adapter, device bluetooth.ScanResult) {
		for _, uuid := range walkingPadUUIDs {
			if device.HasServiceUUID(uuid) {
				if _, ok := set[device.Address.String()]; ok {
					return
				}
				set[device.Address.String()] = struct{}{}

				devices = append(devices, WalkingPadCandidate{Device: device})

				if targetAddr != nil && device.Address.String() == *targetAddr {
					_ = adapter.StopScan()
					return
				}
			}
		}
	})
	if err != nil {
		return nil, err
	}

	return devices, nil
}

func (candidate WalkingPadCandidate) Connect(adapter *bluetooth.Adapter, params bluetooth.ConnectionParams) (*WalkingPad, error) {
	device, err := adapter.Connect(candidate.Device.Address, params)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}

	services, err := device.DiscoverServices(walkingPadUUIDs)
	if err != nil {
		return nil, fmt.Errorf("discover services: %w", err)
	}

	var (
		rxFound, txFound bool
		rx               bluetooth.DeviceCharacteristic
		tx               bluetooth.DeviceCharacteristic
	)
	for _, service := range services {
		characteristics, err := service.DiscoverCharacteristics(nil)
		if err != nil {
			return nil, fmt.Errorf("discover characteristics: %w", err)
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
		return nil, fmt.Errorf("missing characteristics")
	}

	pad := newWalkingPad(device, rx, tx)
	_ = pad.rx.EnableNotifications(pad.onBufferReceive)

	var ctx context.Context
	ctx, pad.cancel = context.WithCancel(context.Background())

	pad.wg.Add(2)
	go pad.writeLoop(ctx)
	go pad.askStatsLoop(ctx)

	return pad, nil
}

type WalkingPad struct {
	device bluetooth.Device
	rx     bluetooth.DeviceCharacteristic
	tx     bluetooth.DeviceCharacteristic

	wg      sync.WaitGroup
	cancel  context.CancelFunc
	stopped bool

	queue chan walkingPadCommand

	LastStatus     WalkingPadStatus
	LastStatusTime time.Time
}

type walkingPadCommand struct {
	timeout time.Duration
	buffer  []byte
}

func newWalkingPad(device bluetooth.Device, rx, tx bluetooth.DeviceCharacteristic) *WalkingPad {
	return &WalkingPad{
		device: device,
		rx:     rx,
		tx:     tx,
		queue:  make(chan walkingPadCommand, 50),
	}
}

func (pad *WalkingPad) Disconnect() {
	if pad.stopped {
		return
	}
	pad.stopped = true

	close(pad.queue)
	pad.cancel()
	pad.wg.Wait()
	_ = pad.device.Disconnect()
}

func (pad *WalkingPad) pushCmd(cmd []byte, timeout time.Duration) {
	fixCrc(cmd)
	pad.queue <- walkingPadCommand{timeout: timeout, buffer: cmd}
}

func (pad *WalkingPad) ChangeMode(mode WalkingPadMode) {
	pad.pushCmd([]byte{247, 162, 2, byte(mode), 0xFF, 253}, 0)
}

func (pad *WalkingPad) StartBelt() {
	pad.pushCmd([]byte{247, 162, 4, 1, 0xFF, 253}, 0)
}

func (pad *WalkingPad) StopBelt() {
	pad.ChangeSpeed(0.0)
}

func (pad *WalkingPad) ChangeSpeed(speed float64) {
	if speed < 0 || speed > 6 {
		panic("invalid speed")
	}
	cnv := byte(speed * 10.0)
	pad.pushCmd([]byte{247, 162, 1, cnv, 0xFF, 253}, 0)
}

func (pad *WalkingPad) AskStats() {
	pad.pushCmd([]byte{247, 162, 0, 0, 162, 253}, 0)
}

func (pad *WalkingPad) WaitCmd(timeout time.Duration) {
	pad.pushCmd(nil, timeout)
}

func (pad *WalkingPad) onBufferReceive(buf []byte) {
	if len(buf) < 2 {
		return
	}

	if buf[0] == 248 && buf[1] == 162 {
		status := readStatusBuffer(buf[2:])
		pad.LastStatus = status
		pad.LastStatusTime = time.Now()
		return
	}
}

func (pad *WalkingPad) writeLoop(ctx context.Context) {
	defer pad.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case cmd := <-pad.queue:
			if cmd.timeout != 0 {
				time.Sleep(cmd.timeout)
			}
			if cmd.buffer != nil {
				_, err := pad.tx.WriteWithoutResponse(cmd.buffer)
				if err != nil {
					slog.Error("error writing to bluetooth device", "err", err)
				}

				time.Sleep(700 * time.Millisecond)
			}
		}
	}
}

func (pad *WalkingPad) askStatsLoop(ctx context.Context) {
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

func fixCrc(cmd []byte) {
	if len(cmd) < 2 {
		return
	}
	var sum byte
	for i := 1; i < len(cmd)-2; i++ {
		sum += cmd[i] // overflow intended
	}
	cmd[len(cmd)-2] = sum
}

type WalkingPadMode byte

const (
	WalkingPadModeStandby WalkingPadMode = 2
	WalkingPadModeManual  WalkingPadMode = 1
	WalkingPadModeAuto    WalkingPadMode = 0
)

type WalkingPadStatus struct {
	Speed    float64
	Mode     WalkingPadMode
	Time     time.Duration
	WalkedKM float64
	Steps    int
}

func readStatusBuffer(buf []byte) WalkingPadStatus {
	timeS := int(buf[3])<<16 | int(buf[4])<<8 | int(buf[5])
	dist := int(buf[6])<<16 | int(buf[7])<<8 | int(buf[8])
	return WalkingPadStatus{
		Speed:    float64(buf[1]) / 10.0,
		Mode:     WalkingPadMode(buf[2]),
		Time:     time.Duration(timeS) * time.Second,
		WalkedKM: float64(dist) / 100.0,
		Steps:    int(buf[9])<<16 | int(buf[10])<<8 | int(buf[11]),
	}
}
