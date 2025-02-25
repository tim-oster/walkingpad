package app

import (
	"fmt"
	"log/slog"
	"sync"

	"github.com/tim-oster/walkingpad/internal"
	"tinygo.org/x/bluetooth"
)

type Walkingpad struct {
	addr string

	padUpdates  <-chan internal.WalkingpadUpdate
	padCommands chan<- internal.WalkingpadCommand

	wg sync.WaitGroup

	mx        sync.Mutex
	lastStats internal.UpdateStats
}

func NewWalkingpadFromCandidate(adapter *bluetooth.Adapter, candidate internal.WalkingpadCandidate) (*Walkingpad, error) {
	updates, cmds, err := candidate.Connect(adapter)
	if err != nil {
		return nil, err
	}

	wp := &Walkingpad{
		addr:        candidate.Device.Address.String(),
		padUpdates:  updates,
		padCommands: cmds,
	}

	wp.wg.Add(1)
	go wp.processUpdates()

	return wp, nil
}

func (wp *Walkingpad) processUpdates() {
	for update := range wp.padUpdates {
		switch update := update.(type) {
		case *internal.UpdateStats:
			wp.mx.Lock()
			wp.lastStats = *update
			wp.mx.Unlock()

		default:
			slog.Error("invalid update type", slog.String("type", fmt.Sprintf("%T", update)))
		}
	}
}

func (wp *Walkingpad) GetStats() internal.UpdateStats {
	wp.mx.Lock()
	defer wp.mx.Unlock()
	return wp.lastStats
}

func (wp *Walkingpad) Disconnect() {
	if wp.padCommands == nil {
		return
	}

	// signal disconnect by closing command channel
	close(wp.padCommands)

	// wait for gorotuines to finish
	wp.wg.Wait()
	wp.padCommands = nil
}

func (wp *Walkingpad) Send(cmd internal.WalkingpadCommand) {
	wp.padCommands <- cmd
}
