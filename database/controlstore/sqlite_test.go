package controlstore

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/internal/credentialcache"
	"github.com/komari-monitor/komari/internal/storage"
	"github.com/komari-monitor/komari/internal/storage/contracttest"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newSQLiteControlFixture(t *testing.T) (contracttest.ControlFixture, *gorm.DB) {
	t.Helper()
	dsn := fmt.Sprintf("file:%s-%d?mode=memory&cache=shared", t.Name(), time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = sqlDB.Close() })
	store, err := NewSQLite(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	user := models.User{
		UUID: "control-user", Username: "control", Passwd: "irrelevant",
		CreatedAt: models.FromTime(now), UpdatedAt: models.FromTime(now),
	}
	client := models.Client{
		UUID: "control-client", Token: "control-token",
		CreatedAt: models.FromTime(now), UpdatedAt: models.FromTime(now),
	}
	session := "control-session"
	digest := credentialcache.Digest(session)
	sessionRecord := models.Session{
		UUID: "control-user", Session: session, SessionDigest: digest[:],
		Expires: models.FromTime(now.Add(time.Hour)), CreatedAt: models.FromTime(now),
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&client).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&sessionRecord).Error; err != nil {
		t.Fatal(err)
	}
	return contracttest.ControlFixture{
		Store: store, ClientToken: client.Token, Session: session,
		Digest: append([]byte(nil), digest[:]...), UserUUID: user.UUID,
	}, db
}

func TestSQLiteControlStoreContract(t *testing.T) {
	contracttest.Control(t, func(t *testing.T) contracttest.ControlFixture {
		fixture, _ := newSQLiteControlFixture(t)
		return fixture
	})
}

func TestSQLiteControlStoreBackfillsLegacySessionDigest(t *testing.T) {
	fixture, db := newSQLiteControlFixture(t)
	legacy := models.Session{
		UUID: fixture.UserUUID, Session: "legacy-session",
		Expires: models.FromTime(time.Now().Add(time.Hour)), CreatedAt: models.FromTime(time.Now()),
	}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	digest := credentialcache.Digest(legacy.Session)
	got, err := fixture.Store.SessionCredential(context.Background(), legacy.Session, digest[:])
	if err != nil || got.UUID != fixture.UserUUID {
		t.Fatalf("session=%+v err=%v", got, err)
	}
	var persisted models.Session
	if err := db.Where("session = ?", legacy.Session).First(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if string(persisted.SessionDigest) != string(digest[:]) {
		t.Fatal("legacy digest was not backfilled")
	}
}

func TestSQLiteControlStoreHealthFailureAndInvalidInputs(t *testing.T) {
	if _, err := NewSQLite(nil); err == nil {
		t.Fatal("nil database accepted")
	}
	fixture, db := newSQLiteControlFixture(t)
	if _, err := fixture.Store.ClientCredential(context.Background(), ""); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("empty token error=%v", err)
	}
	if _, err := fixture.Store.SessionCredential(context.Background(), "", nil); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("empty session error=%v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	health, err := fixture.Store.Health(context.Background())
	if err == nil || health.Ready {
		t.Fatalf("health=%+v err=%v", health, err)
	}
}
