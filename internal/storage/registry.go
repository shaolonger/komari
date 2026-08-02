package storage

import (
	"context"
	"errors"
	"sync/atomic"
)

type telemetryHolder struct {
	store TelemetryStore
}

type controlHolder struct {
	store ControlStore
}

var (
	telemetryBackend atomic.Pointer[telemetryHolder]
	controlBackend   atomic.Pointer[controlHolder]
)

// InstallTelemetry atomically publishes a backend and returns an idempotent
// restore function intended for tests and bounded lifecycle ownership.
func InstallTelemetry(store TelemetryStore) (func(), error) {
	if store == nil {
		return nil, errors.New("telemetry store is required")
	}
	next := &telemetryHolder{store: store}
	previous := telemetryBackend.Swap(next)
	var restored atomic.Bool
	return func() {
		if restored.CompareAndSwap(false, true) {
			telemetryBackend.CompareAndSwap(next, previous)
		}
	}, nil
}

func Telemetry() (TelemetryStore, bool) {
	current := telemetryBackend.Load()
	if current == nil || current.store == nil {
		return nil, false
	}
	return current.store, true
}

func RequireTelemetry() (TelemetryStore, error) {
	store, ok := Telemetry()
	if !ok {
		return nil, ErrNotConfigured
	}
	return store, nil
}

func InstallControl(store ControlStore) (func(), error) {
	if store == nil {
		return nil, errors.New("control store is required")
	}
	next := &controlHolder{store: store}
	previous := controlBackend.Swap(next)
	var restored atomic.Bool
	return func() {
		if restored.CompareAndSwap(false, true) {
			controlBackend.CompareAndSwap(next, previous)
		}
	}, nil
}

func Control() (ControlStore, bool) {
	current := controlBackend.Load()
	if current == nil || current.store == nil {
		return nil, false
	}
	return current.store, true
}

func RequireControl() (ControlStore, error) {
	store, ok := Control()
	if !ok {
		return nil, ErrNotConfigured
	}
	return store, nil
}

func Close(ctx context.Context) error {
	var errs []error
	if telemetry, ok := Telemetry(); ok {
		errs = append(errs, telemetry.Close(ctx))
	}
	if control, ok := Control(); ok {
		errs = append(errs, control.Close(ctx))
	}
	return errors.Join(errs...)
}
