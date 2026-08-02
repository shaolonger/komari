package accounts

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/internal/credentialcache"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func insertSessionCredential(t testing.TB, expires time.Time) models.Session {
	t.Helper()
	now := time.Now()
	userID := uuid.NewString()
	user := models.User{
		UUID: userID, Username: "fixture-" + userID[:8], Passwd: "fixture-only-not-a-login-hash",
		CreatedAt: models.FromTime(now), UpdatedAt: models.FromTime(now),
	}
	if err := dbcore.GetDBInstance().Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	record := models.Session{
		UUID:      userID,
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
		_ = dbcore.GetDBInstance().Delete(&models.User{}, "uuid = ?", userID).Error
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

func TestLegacySessionDigestBackfillAndIndexes(t *testing.T) {
	resetSessionCredentialCacheForTest()
	record := insertSessionCredential(t, time.Now().Add(time.Hour))
	if _, err := GetSession(record.Session); err != nil {
		t.Fatal(err)
	}
	var stored models.Session
	if err := dbcore.GetDBInstance().Select("session_digest").Where("session = ?", record.Session).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if len(stored.SessionDigest) != 32 {
		t.Fatalf("legacy digest length = %d, want 32", len(stored.SessionDigest))
	}

	var indexes []struct{ Name string }
	if err := dbcore.GetDBInstance().Raw("PRAGMA index_list('sessions')").Scan(&indexes).Error; err != nil {
		t.Fatal(err)
	}
	found := make(map[string]bool)
	for _, index := range indexes {
		found[index.Name] = true
	}
	for _, name := range []string{"idx_sessions_digest", "idx_sessions_expires", "idx_sessions_uuid"} {
		if !found[name] {
			t.Fatalf("missing session index %q; indexes=%v", name, found)
		}
	}
}

func TestSQLiteActivityStoreWritesActiveAndSkipsExpiredSession(t *testing.T) {
	db := dbcore.GetDBInstance()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	store := &sqliteActivityStore{db: sqlDB}
	tracker := manualActivityTracker(t, store, 32)

	active := insertSessionCredential(t, time.Now().Add(time.Hour))
	if _, err := GetSession(active.Session); err != nil {
		t.Fatal(err)
	}
	seen := time.Now().Add(time.Second).Truncate(time.Millisecond)
	if err := tracker.Touch(active.Session, "changed-agent", "198.51.100.7", seen); err != nil {
		t.Fatal(err)
	}
	if err := tracker.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	var stored models.Session
	if err := db.Where("session = ?", active.Session).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.LatestUserAgent != "changed-agent" || stored.LatestIp != "198.51.100.7" || stored.LatestOnline.ToTime().Before(seen.Add(-time.Millisecond)) {
		t.Fatalf("stored activity = %+v", stored)
	}

	expiredToken := "expired-activity-" + uuid.NewString()
	digest := credentialcache.Digest(expiredToken)
	expired := models.Session{
		UUID: active.UUID, Session: expiredToken, SessionDigest: append([]byte(nil), digest[:]...),
		Expires: models.FromTime(time.Now().Add(-time.Minute)), CreatedAt: models.FromTime(time.Now()),
	}
	if err := db.Create(&expired).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Delete(&models.Session{}, "session = ?", expiredToken).Error })
	if err := tracker.Touch(expiredToken, "must-not-write", "203.0.113.9", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := tracker.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored = models.Session{}
	if err := db.Where("session = ?", expiredToken).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.LatestUserAgent != "" || stored.LatestIp != "" {
		t.Fatalf("expired activity was written: %+v", stored)
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

func BenchmarkSessionActivityUpdate(b *testing.B) {
	record := insertSessionCredential(b, time.Now().Add(time.Hour))
	if _, err := GetSession(record.Session); err != nil {
		b.Fatal(err)
	}
	db := dbcore.GetDBInstance().Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
	b.Run("database-per-request", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if err := db.Model(&models.Session{}).Where("session = ?", record.Session).Updates(map[string]interface{}{
				"latest_online": time.Now(), "latest_user_agent": "benchmark-agent", "latest_ip": "192.0.2.1",
			}).Error; err != nil {
				b.Fatal(err)
			}
		}
	})
	store := &fakeActivityStore{}
	tracker := manualActivityTracker(b, store, 128)
	base := time.Now()
	if err := tracker.Touch(record.Session, "benchmark-agent", "192.0.2.1", base); err != nil {
		b.Fatal(err)
	}
	if err := tracker.Flush(context.Background()); err != nil {
		b.Fatal(err)
	}
	b.Run("coalesced-touch", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if err := tracker.Touch(record.Session, "benchmark-agent", "192.0.2.1", base.Add(time.Second)); err != nil {
				b.Fatal(err)
			}
		}
	})
}
