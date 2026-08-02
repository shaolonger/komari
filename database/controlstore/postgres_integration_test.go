//go:build integration && scale

package controlstore

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/internal/storage/contracttest"
)

func TestPostgresControlStoreIntegration(t *testing.T) {
	url := os.Getenv("KOMARI_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("KOMARI_TEST_POSTGRES_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := NewPostgres(ctx, PostgresConfig{
		URL: url, MaxConns: 16, MinConns: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		_ = store.Close(closeCtx)
	})
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `
		TRUNCATE komari_sessions, komari_clients, komari_users CASCADE
	`); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	user := models.User{
		UUID: "pg-user", Username: "pg-user", Passwd: "hash", SSOID: "pg-sso",
		CreatedAt: models.FromTime(now), UpdatedAt: models.FromTime(now),
	}
	client := models.Client{
		UUID: "pg-client", Token: "pg-token",
		CreatedAt: models.FromTime(now), UpdatedAt: models.FromTime(now),
	}
	session := models.Session{
		UUID: user.UUID, SessionDigest: []byte("pg-session-digest"),
		Expires: models.FromTime(now.Add(time.Hour)), CreatedAt: models.FromTime(now),
	}
	if err := store.UpsertUserAuth(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertClientAuth(ctx, client); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSessionAuth(ctx, session); err != nil {
		t.Fatal(err)
	}

	contracttest.Control(t, func(*testing.T) contracttest.ControlFixture {
		return contracttest.ControlFixture{
			Store: store, ClientToken: client.Token, Session: "not-persisted",
			Digest: session.SessionDigest, UserUUID: user.UUID,
		}
	})

	t.Run("concurrent migration lock", func(t *testing.T) {
		var wait sync.WaitGroup
		errs := make(chan error, 8)
		for range 8 {
			wait.Add(1)
			go func() {
				defer wait.Done()
				errs <- store.Migrate(context.Background())
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

	t.Run("connection recovery", func(t *testing.T) {
		killer, err := pgx.Connect(context.Background(), url)
		if err != nil {
			t.Fatal(err)
		}
		defer killer.Close(context.Background())
		if _, err := killer.Exec(context.Background(), `
			SELECT pg_terminate_backend(pid)
			FROM pg_stat_activity
			WHERE datname=current_database() AND pid <> pg_backend_pid()
		`); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for {
			health, err := store.Health(context.Background())
			if err == nil && health.Ready {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("pool did not recover: health=%+v err=%v", health, err)
			}
			time.Sleep(50 * time.Millisecond)
		}
	})

	t.Run("parallel credential stress", func(t *testing.T) {
		var wait sync.WaitGroup
		errs := make(chan error, 128)
		for range 128 {
			wait.Add(1)
			go func() {
				defer wait.Done()
				for range 20 {
					if _, err := store.ClientCredential(context.Background(), client.Token); err != nil {
						errs <- err
						return
					}
				}
				errs <- nil
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

func TestPostgresControlStoreTLSIntegration(t *testing.T) {
	url := os.Getenv("KOMARI_TEST_POSTGRES_TLS_URL")
	if url == "" {
		t.Skip("KOMARI_TEST_POSTGRES_TLS_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store, err := NewPostgres(ctx, PostgresConfig{
		URL: url, RequireTLS: true, MaxConns: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close(context.Background())
	health, err := store.Health(ctx)
	if err != nil || !health.Ready {
		t.Fatalf("health=%+v err=%v", health, err)
	}
}
