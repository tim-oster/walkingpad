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

type UpadteStats struct {
	WalkingpadUpdate

	Speed    float64
	Time     time.Duration
	WalkedKM float64
	Steps    int
}

// ---------------------------------------------------------------------------------------------------------------

type WalkingpadCandidate struct {
	Device  bluetooth.ScanResult
	Connect func(adapter *bluetooth.Adapter, candidate WalkingpadCandidate) (<-chan WalkingpadUpdate, chan<- WalkingpadCommand, error)
}

var WalkingpadDiscoverer []func(adapter *bluetooth.Adapter, device bluetooth.ScanResult) (WalkingpadCandidate, bool)

func DiscoverWalkingpadCandidates(adapter *bluetooth.Adapter, timeout time.Duration, targetAddr *string) ([]WalkingpadCandidate, error) {
	go func() {
		<-time.After(timeout)
		_ = adapter.StopScan()
	}()

	var (
		set        = make(map[string]struct{})
		candidates []WalkingpadCandidate
	)
	err := adapter.Scan(func(adapter *bluetooth.Adapter, device bluetooth.ScanResult) {
		for _, d := range WalkingpadDiscoverer {
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
