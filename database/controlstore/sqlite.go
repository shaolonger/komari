// Package controlstore contains strongly consistent control-plane storage
// adapters. Caches live above this package and never become authentication
// authorities themselves.
package controlstore

import (
	"context"
	"errors"
	"time"

	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/internal/storage"
	"gorm.io/gorm"
)

type SQLite struct {
	db *gorm.DB
}

var _ storage.ControlStore = (*SQLite)(nil)

func NewSQLite(db *gorm.DB) (*SQLite, error) {
	if db == nil {
		return nil, errors.New("SQLite control database is required")
	}
	return &SQLite{db: db}, nil
}

func (store *SQLite) ClientCredential(ctx context.Context, token string) (models.Client, error) {
	if token == "" {
		return models.Client{}, storage.ErrNotFound
	}
	var client models.Client
	err := store.db.WithContext(ctx).
		Select("uuid", "token_expires_at", "token_revoked_at", "updated_at").
		Where("token = ?", token).First(&client).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.Client{}, storage.ErrNotFound
	}
	return client, err
}

func (store *SQLite) SessionCredential(ctx context.Context, session string, digest []byte) (models.Session, error) {
	if session == "" || len(digest) == 0 {
		return models.Session{}, storage.ErrNotFound
	}
	var credential models.Session
	err := store.db.WithContext(ctx).Select("uuid", "expires", "created_at").
		Where("session_digest = ?", digest).First(&credential).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// Legacy databases stored only the session token. Backfill its digest on
		// the first successful lookup without ever logging either value.
		err = store.db.WithContext(ctx).Select("uuid", "expires", "created_at").
			Where("session = ?", session).First(&credential).Error
		if err == nil {
			err = store.db.WithContext(ctx).Model(&models.Session{}).
				Where("session = ?", session).Update("session_digest", append([]byte(nil), digest...)).Error
		}
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.Session{}, storage.ErrNotFound
	}
	return credential, err
}

func (store *SQLite) UserByUUID(ctx context.Context, uuid string) (models.User, error) {
	if uuid == "" {
		return models.User{}, storage.ErrNotFound
	}
	var user models.User
	err := store.db.WithContext(ctx).Where("uuid = ?", uuid).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.User{}, storage.ErrNotFound
	}
	return user, err
}

func (store *SQLite) Migrate(ctx context.Context) error {
	return store.db.WithContext(ctx).AutoMigrate(&models.User{}, &models.Client{}, &models.Session{})
}

func (store *SQLite) Health(ctx context.Context) (storage.Health, error) {
	started := time.Now()
	health := storage.Health{Backend: "sqlite", CheckedAt: started}
	var value int
	err := store.db.WithContext(ctx).Raw("SELECT 1").Scan(&value).Error
	health.Latency = time.Since(started)
	health.Ready = err == nil && value == 1
	if err != nil {
		return health, err
	}
	if !health.Ready {
		return health, errors.New("SQLite control health query returned an unexpected value")
	}
	return health, nil
}

// The pool is owned by dbcore and is shared with the rest of the SQLite
// control plane, so closing this lightweight adapter is intentionally a no-op.
func (store *SQLite) Close(context.Context) error { return nil }
