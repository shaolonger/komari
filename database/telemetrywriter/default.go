package telemetrywriter

import (
	"context"
	"sync"

	"github.com/komari-monitor/komari/database/dbcore"
)

var (
	defaultMu     sync.Mutex
	defaultWriter *Writer
	defaultClosed bool
)

func Default() (*Writer, error) {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	if defaultClosed {
		return nil, ErrClosed
	}
	if defaultWriter != nil {
		return defaultWriter, nil
	}
	sqlDB, err := dbcore.GetWriterDBInstance()
	if err != nil {
		return nil, err
	}
	defaultWriter, err = New(sqlDB, Config{})
	return defaultWriter, err
}

func Submit(ctx context.Context, batch Batch) error {
	writer, err := Default()
	if err != nil {
		return err
	}
	return writer.Submit(ctx, batch)
}

func CloseDefault(ctx context.Context) error {
	defaultMu.Lock()
	writer := defaultWriter
	defaultWriter = nil
	defaultClosed = true
	defaultMu.Unlock()
	if writer == nil {
		return nil
	}
	return writer.Close(ctx)
}
