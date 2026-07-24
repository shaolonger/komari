//go:build integration && scale

package controlstore_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/accounts"
	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/database/controlstore"
	"github.com/komari-monitor/komari/internal/storage"
	"gorm.io/gorm"
)

func TestPostgresAuthorityApplicationLifecycle(t *testing.T) {
	url := os.Getenv("KOMARI_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("KOMARI_TEST_POSTGRES_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := controlstore.NewPostgres(ctx, controlstore.PostgresConfig{
		URL: url, MaxConns: 8, MinConns: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	restore, err := storage.InstallControl(store)
	if err != nil {
		t.Fatal(err)
	}
	suffix := fmt.Sprint(time.Now().UnixNano())
	username := "authority-" + suffix
	var clientUUID string
	t.Cleanup(func() {
		if clientUUID != "" {
			_ = clients.DeleteClient(clientUUID)
		}
		_ = accounts.DeleteAccountByUsername(username)
		restore()
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		_ = store.Close(closeCtx)
	})

	user, err := accounts.CreateAccount(username, "first-password")
	if err != nil {
		t.Fatal(err)
	}
	if uuid, ok := accounts.CheckPassword(username, "first-password"); !ok || uuid != user.UUID {
		t.Fatalf("password authority uuid=%q ok=%v", uuid, ok)
	}
	if err := accounts.Enable2Fa(user.UUID, "JBSWY3DPEHPK3PXP"); err != nil {
		t.Fatal(err)
	}
	authoritativeUser, err := store.UserByUUID(ctx, user.UUID)
	if err != nil || authoritativeUser.TwoFactor == "" {
		t.Fatalf("user=%+v err=%v", authoritativeUser, err)
	}
	session, err := accounts.CreateSession(user.UUID, 3600, "integration", "127.0.0.1", "password")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := accounts.GetSession(session); err != nil || got != user.UUID {
		t.Fatalf("session user=%q err=%v", got, err)
	}
	if err := accounts.ForceResetPassword(username, "second-password"); err != nil {
		t.Fatal(err)
	}
	if _, ok := accounts.CheckPassword(username, "first-password"); ok {
		t.Fatal("old password remained valid")
	}
	if uuid, ok := accounts.CheckPassword(username, "second-password"); !ok || uuid != user.UUID {
		t.Fatalf("new password authority uuid=%q ok=%v", uuid, ok)
	}
	if _, err := accounts.GetSession(session); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("revoked session error=%v", err)
	}

	clientUUID, token, err := clients.CreateClientWithName("authority-client")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := clients.GetClientUUIDByToken(token); err != nil || got != clientUUID {
		t.Fatalf("client uuid=%q err=%v", got, err)
	}
	status, err := clients.RotateClientToken(clientUUID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clients.GetClientUUIDByToken(token); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("old token error=%v", err)
	}
	if got, err := clients.GetClientUUIDByToken(status.Token); err != nil || got != clientUUID {
		t.Fatalf("rotated token uuid=%q err=%v", got, err)
	}
	if _, err := clients.RevokeClientToken(clientUUID); err != nil {
		t.Fatal(err)
	}
	if _, err := clients.GetClientUUIDByToken(status.Token); !errors.Is(err, clients.ErrClientTokenRevoked) {
		t.Fatalf("revoked token error=%v", err)
	}
}
