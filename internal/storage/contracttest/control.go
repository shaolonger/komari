package contracttest

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/komari-monitor/komari/internal/storage"
)

type ControlFixture struct {
	Store       storage.ControlStore
	ClientToken string
	Session     string
	Digest      []byte
	UserUUID    string
}

type ControlFactory func(*testing.T) ControlFixture

func Control(t *testing.T, factory ControlFactory) {
	t.Helper()
	fixture := factory(t)

	t.Run("migration and health", func(t *testing.T) {
		if err := fixture.Store.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := fixture.Store.Migrate(context.Background()); err != nil {
			t.Fatalf("idempotent migration: %v", err)
		}
		health, err := fixture.Store.Health(context.Background())
		if err != nil || !health.Ready || health.Backend == "" {
			t.Fatalf("health=%+v err=%v", health, err)
		}
	})

	t.Run("credential authority", func(t *testing.T) {
		client, err := fixture.Store.ClientCredential(context.Background(), fixture.ClientToken)
		if err != nil || client.UUID == "" {
			t.Fatalf("client=%+v err=%v", client, err)
		}
		session, err := fixture.Store.SessionCredential(context.Background(), fixture.Session, fixture.Digest)
		if err != nil || session.UUID != fixture.UserUUID {
			t.Fatalf("session=%+v err=%v", session, err)
		}
		user, err := fixture.Store.UserByUUID(context.Background(), fixture.UserUUID)
		if err != nil || user.UUID != fixture.UserUUID {
			t.Fatalf("user=%+v err=%v", user, err)
		}
		if _, err := fixture.Store.ClientCredential(context.Background(), "missing"); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("missing client error=%v", err)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := fixture.Store.ClientCredential(ctx, fixture.ClientToken); !errors.Is(err, context.Canceled) {
			t.Fatalf("client error=%v, want context canceled", err)
		}
		if _, err := fixture.Store.UserByUUID(ctx, fixture.UserUUID); !errors.Is(err, context.Canceled) {
			t.Fatalf("user error=%v, want context canceled", err)
		}
	})

	t.Run("concurrent reads", func(t *testing.T) {
		var wait sync.WaitGroup
		errs := make(chan error, 64)
		for range 64 {
			wait.Add(1)
			go func() {
				defer wait.Done()
				_, err := fixture.Store.ClientCredential(context.Background(), fixture.ClientToken)
				errs <- err
			}()
		}
		wait.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
	})
}
