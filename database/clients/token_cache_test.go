package clients

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func insertCredentialClient(t testing.TB, token string, expiresAt time.Time) models.Client {
	t.Helper()
	now := time.Now()
	client := models.Client{
		UUID:           uuid.NewString(),
		Token:          token,
		TokenIssuedAt:  models.FromTime(now),
		TokenExpiresAt: models.FromTime(expiresAt),
		CreatedAt:      models.FromTime(now),
		UpdatedAt:      models.FromTime(now),
	}
	if err := dbcore.GetDBInstance().Create(&client).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		invalidateClientCredential(token)
		_ = dbcore.GetDBInstance().Delete(&models.Client{}, "uuid = ?", client.UUID).Error
	})
	return client
}

func TestClientCredentialCacheHitAndExplicitInvalidation(t *testing.T) {
	resetClientCredentialCacheForTest()
	token := "hit-" + uuid.NewString()
	client := insertCredentialClient(t, token, time.Time{})

	if got, err := GetClientUUIDByToken(token); err != nil || got != client.UUID {
		t.Fatalf("first lookup = (%q, %v)", got, err)
	}
	if err := dbcore.GetDBInstance().Delete(&models.Client{}, "uuid = ?", client.UUID).Error; err != nil {
		t.Fatal(err)
	}
	if got, err := GetClientUUIDByToken(token); err != nil || got != client.UUID {
		t.Fatalf("cached lookup = (%q, %v), want cached UUID", got, err)
	}
	invalidateClientCredential(token)
	if _, err := GetClientUUIDByToken(token); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("lookup after invalidation error = %v, want record not found", err)
	}
}

func TestClientCredentialExpiryRotationRevocationAndDeletion(t *testing.T) {
	t.Run("expiry", func(t *testing.T) {
		resetClientCredentialCacheForTest()
		token := "expired-" + uuid.NewString()
		insertCredentialClient(t, token, time.Now().Add(-time.Second))
		if _, err := GetClientUUIDByToken(token); !errors.Is(err, ErrClientTokenExpired) {
			t.Fatalf("error = %v, want expired", err)
		}
	})

	t.Run("rotation", func(t *testing.T) {
		resetClientCredentialCacheForTest()
		oldToken := "rotate-" + uuid.NewString()
		client := insertCredentialClient(t, oldToken, time.Time{})
		if _, err := GetClientUUIDByToken(oldToken); err != nil {
			t.Fatal(err)
		}
		status, err := RotateClientToken(client.UUID, 1)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := GetClientUUIDByToken(oldToken); !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatalf("old token error = %v, want record not found", err)
		}
		if got, err := GetClientUUIDByToken(status.Token); err != nil || got != client.UUID {
			t.Fatalf("new token lookup = (%q, %v)", got, err)
		}
	})

	t.Run("revocation", func(t *testing.T) {
		resetClientCredentialCacheForTest()
		token := "revoke-" + uuid.NewString()
		client := insertCredentialClient(t, token, time.Time{})
		if _, err := GetClientUUIDByToken(token); err != nil {
			t.Fatal(err)
		}
		if _, err := RevokeClientToken(client.UUID); err != nil {
			t.Fatal(err)
		}
		if _, err := GetClientUUIDByToken(token); !errors.Is(err, ErrClientTokenRevoked) {
			t.Fatalf("error = %v, want revoked", err)
		}
	})

	t.Run("deletion", func(t *testing.T) {
		resetClientCredentialCacheForTest()
		token := "delete-" + uuid.NewString()
		client := insertCredentialClient(t, token, time.Time{})
		db := dbcore.GetDBInstance()
		if err := db.Create(&models.OfflineNotification{
			Client: client.UUID,
			Enable: true,
		}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&models.TrafficReportNotification{
			Client: client.UUID,
			Enable: true,
			Daily:  true,
		}).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := GetClientUUIDByToken(token); err != nil {
			t.Fatal(err)
		}
		if err := DeleteClient(client.UUID); err != nil {
			t.Fatal(err)
		}
		for name, model := range map[string]interface{}{
			"offline notification": &models.OfflineNotification{},
			"traffic notification": &models.TrafficReportNotification{},
		} {
			var count int64
			if err := db.Model(model).Where("client = ?", client.UUID).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("%s rows after client deletion = %d, want 0", name, count)
			}
		}
		if _, err := GetClientUUIDByToken(token); !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatalf("error = %v, want record not found", err)
		}
	})
}

func TestClientCredentialConcurrentReadsAfterRotation(t *testing.T) {
	resetClientCredentialCacheForTest()
	oldToken := "concurrent-" + uuid.NewString()
	client := insertCredentialClient(t, oldToken, time.Time{})
	if _, err := GetClientUUIDByToken(oldToken); err != nil {
		t.Fatal(err)
	}
	status, err := RotateClientToken(client.UUID, 0)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for worker := 0; worker < 32; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				if _, err := GetClientUUIDByToken(oldToken); !errors.Is(err, gorm.ErrRecordNotFound) {
					errs <- fmt.Errorf("old credential accepted: %v", err)
					return
				}
				if got, err := GetClientUUIDByToken(status.Token); err != nil || got != client.UUID {
					errs <- fmt.Errorf("new credential rejected: uuid=%q err=%v", got, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func BenchmarkClientCredentialLookup(b *testing.B) {
	resetClientCredentialCacheForTest()
	token := "benchmark-" + uuid.NewString()
	client := insertCredentialClient(b, token, time.Time{})
	db := dbcore.GetDBInstance().Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
	b.Run("database", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			var row models.Client
			if err := db.Select("uuid", "token_expires_at", "token_revoked_at", "updated_at").Where("token = ?", token).First(&row).Error; err != nil {
				b.Fatal(err)
			}
		}
	})
	if got, err := GetClientUUIDByToken(token); err != nil || got != client.UUID {
		b.Fatal(got, err)
	}
	b.Run("digest-cache", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := GetClientUUIDByToken(token); err != nil {
				b.Fatal(err)
			}
		}
	})
}
