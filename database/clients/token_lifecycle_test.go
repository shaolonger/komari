package clients

import (
	"errors"
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/models"
)

func TestValidateClientTokenStateAllowsActiveToken(t *testing.T) {
	now := time.Date(2026, time.May, 26, 12, 0, 0, 0, time.UTC)
	client := models.Client{
		TokenIssuedAt: models.FromTime(now.Add(-time.Hour)),
	}

	if err := validateClientTokenState(client, now); err != nil {
		t.Fatalf("validateClientTokenState() error = %v, want nil", err)
	}
}

func TestValidateClientTokenStateRejectsRevokedToken(t *testing.T) {
	now := time.Date(2026, time.May, 26, 12, 0, 0, 0, time.UTC)
	client := models.Client{
		TokenRevokedAt: models.FromTime(now.Add(-time.Minute)),
	}

	err := validateClientTokenState(client, now)
	if !errors.Is(err, ErrClientTokenRevoked) {
		t.Fatalf("validateClientTokenState() error = %v, want %v", err, ErrClientTokenRevoked)
	}
}

func TestValidateClientTokenStateRejectsExpiredToken(t *testing.T) {
	now := time.Date(2026, time.May, 26, 12, 0, 0, 0, time.UTC)
	client := models.Client{
		TokenExpiresAt: models.FromTime(now.Add(-time.Minute)),
	}

	err := validateClientTokenState(client, now)
	if !errors.Is(err, ErrClientTokenExpired) {
		t.Fatalf("validateClientTokenState() error = %v, want %v", err, ErrClientTokenExpired)
	}
}

func TestClientTokenExpiryFromHours(t *testing.T) {
	now := time.Date(2026, time.May, 26, 12, 0, 0, 0, time.UTC)

	expiresAt, err := clientTokenExpiryFromHours(24, now)
	if err != nil {
		t.Fatalf("clientTokenExpiryFromHours() error = %v", err)
	}
	if got := expiresAt.ToTime(); !got.Equal(now.Add(24 * time.Hour)) {
		t.Fatalf("expiresAt = %v, want %v", got, now.Add(24*time.Hour))
	}

	neverExpires, err := clientTokenExpiryFromHours(0, now)
	if err != nil {
		t.Fatalf("clientTokenExpiryFromHours(0) error = %v", err)
	}
	if !neverExpires.ToTime().IsZero() {
		t.Fatalf("neverExpires = %v, want zero time", neverExpires.ToTime())
	}

	if _, err := clientTokenExpiryFromHours(-1, now); err == nil {
		t.Fatal("expected negative expires_in_hours to be rejected")
	}
}