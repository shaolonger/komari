package accounts

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

func insertSessionCredential(t testing.TB, expires time.Time) models.Session {
	t.Helper()
	now := time.Now()
	record := models.Session{
		UUID:      uuid.NewString(),
		Session:   "session-" + uuid.NewString(),
		Expires:   models.FromTime(expires),
		CreatedAt: models.FromTime(now),
	}
	if err := dbcore.GetDBInstance().Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		invalidateSessionCredential(record.Session)
		_ = dbcore.GetDBInstance().Delete(&models.Session{}, "session = ?", record.Session).Error
	})
	return record
}

func TestSessionCredentialCacheHitAndInvalidation(t *testing.T) {
	resetSessionCredentialCacheForTest()
	record := insertSessionCredential(t, time.Now().Add(time.Hour))
	if got, err := GetSession(record.Session); err != nil || got != record.UUID {
		t.Fatalf("first lookup = (%q, %v)", got, err)
	}
	if err := dbcore.GetDBInstance().Delete(&models.Session{}, "session = ?", record.Session).Error; err != nil {
		t.Fatal(err)
	}
	if got, err := GetSession(record.Session); err != nil || got != record.UUID {
		t.Fatalf("cached lookup = (%q, %v)", got, err)
	}
	invalidateSessionCredential(record.Session)
	if _, err := GetSession(record.Session); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("lookup after invalidation error = %v", err)
	}
}

func TestSessionExpiryDeletionAndConcurrentRevocation(t *testing.T) {
	t.Run("expiry", func(t *testing.T) {
		resetSessionCredentialCacheForTest()
		record := insertSessionCredential(t, time.Now().Add(-time.Second))
		if _, err := GetSession(record.Session); !errors.Is(err, ErrSessionExpired) {
			t.Fatalf("error = %v, want expired", err)
		}
		var count int64
		if err := dbcore.GetDBInstance().Model(&models.Session{}).Where("session = ?", record.Session).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("expired session remained: count=%d err=%v", count, err)
		}
	})

	t.Run("concurrent delete", func(t *testing.T) {
		resetSessionCredentialCacheForTest()
		record := insertSessionCredential(t, time.Now().Add(time.Hour))
		if _, err := GetSession(record.Session); err != nil {
			t.Fatal(err)
		}
		if err := DeleteSession(record.Session); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		errs := make(chan error, 32)
		for worker := 0; worker < 32; worker++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 200; i++ {
					if _, err := GetSession(record.Session); !errors.Is(err, gorm.ErrRecordNotFound) {
						errs <- fmt.Errorf("deleted session accepted: %v", err)
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
	})
}

func TestSessionAccountPasswordChangeRevokesOnlyAccountSessions(t *testing.T) {
	resetSessionCredentialCacheForTest()
	user, err := CreateAccount("user-"+uuid.NewString(), "before-password")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = DeleteAccountByUsername(user.Username) })
	record := insertSessionCredential(t, time.Now().Add(time.Hour))
	if err := dbcore.GetDBInstance().Model(&models.Session{}).Where("session = ?", record.Session).Update("uuid", user.UUID).Error; err != nil {
		t.Fatal(err)
	}
	resetSessionCredentialCacheForTest()
	if _, err := GetSession(record.Session); err != nil {
		t.Fatal(err)
	}
	password := "after-password"
	if err := UpdateUser(user.UUID, nil, &password, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := GetSession(record.Session); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("session after password change error = %v", err)
	}
}

func BenchmarkSessionCredentialLookup(b *testing.B) {
	resetSessionCredentialCacheForTest()
	record := insertSessionCredential(b, time.Now().Add(time.Hour))
	db := dbcore.GetDBInstance().Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
	b.Run("database", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			var row models.Session
			if err := db.Select("uuid", "expires", "created_at").Where("session = ?", record.Session).First(&row).Error; err != nil {
				b.Fatal(err)
			}
		}
	})
	if _, err := GetSession(record.Session); err != nil {
		b.Fatal(err)
	}
	b.Run("digest-cache", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := GetSession(record.Session); err != nil {
				b.Fatal(err)
			}
		}
	})
}
