package internal

import (
	"fmt"
	"time"

	"tinygo.org/x/bluetooth"
)

type WalkingpadCommand interface {
	isWalkingpadCommand()
}

type CmdStart struct {
	WalkingpadCommand
	Speed float64
}

type CmdStop struct {
	WalkingpadCommand
}

type CmdChangeSpeed struct {
	WalkingpadCommand
	Speed float64
}

// ---------------------------------------------------------------------------------------------------------------

type WalkingpadUpdate interface {
	isWalkingpadUpdate()
}

type UpdateStats struct {
	WalkingpadUpdate

	Timestamp time.Time
	Speed     float64
	Time      time.Duration
	WalkedKM  float64
	Steps     int
}

// ---------------------------------------------------------------------------------------------------------------

type WalkingpadCandidate struct {
	Device    bluetooth.ScanResult
	ConnectFn func(adapter *bluetooth.Adapter, candidate WalkingpadCandidate) (<-chan WalkingpadUpdate, chan<- WalkingpadCommand, error)
}

func (candidate WalkingpadCandidate) Connect(adapter *bluetooth.Adapter) (<-chan WalkingpadUpdate, chan<- WalkingpadCommand, error) {
	return candidate.ConnectFn(adapter, candidate)
}

type WalkingpadDiscovererFn func(adapter *bluetooth.Adapter, device bluetooth.ScanResult) (WalkingpadCandidate, bool)

func DiscoverWalkingpadCandidates(adapter *bluetooth.Adapter, timeout time.Duration, discoverFns []WalkingpadDiscovererFn, targetAddr *string) ([]WalkingpadCandidate, error) {
	go func() {
		<-time.After(timeout)
		_ = adapter.StopScan()
	}()

	var (
		set        = make(map[string]struct{})
		candidates []WalkingpadCandidate
	)
	err := adapter.Scan(func(adapter *bluetooth.Adapter, device bluetooth.ScanResult) {
		for _, d := range discoverFns {
			if _, ok := set[device.Address.String()]; ok {
				return
			}
			set[device.Address.String()] = struct{}{}

			candidate, ok := d(adapter, device)
			if !ok {
				continue
			}
			candidates = append(candidates, candidate)

			if targetAddr != nil && device.Address.String() == *targetAddr {
				_ = adapter.StopScan()
				return
			}
		}

	})
	if err != nil {
		return nil, fmt.Errorf("error discovering walkingpads: %w", err)
	}

	return candidates, nil
}
