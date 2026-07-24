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
	"gorm.io/gorm/clause"
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

func (store *SQLite) ClientByUUID(ctx context.Context, uuid string) (models.Client, error) {
	if uuid == "" {
		return models.Client{}, storage.ErrNotFound
	}
	var client models.Client
	err := store.db.WithContext(ctx).
		Select("uuid", "token", "token_issued_at", "token_expires_at", "token_revoked_at", "created_at", "updated_at").
		Where("uuid = ?", uuid).First(&client).Error
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

func (store *SQLite) UserByUsername(ctx context.Context, username string) (models.User, error) {
	if username == "" {
		return models.User{}, storage.ErrNotFound
	}
	var user models.User
	err := store.db.WithContext(ctx).Where("username = ?", username).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.User{}, storage.ErrNotFound
	}
	return user, err
}

func (store *SQLite) UserBySSO(ctx context.Context, ssoID string) (models.User, error) {
	if ssoID == "" {
		return models.User{}, storage.ErrNotFound
	}
	var user models.User
	err := store.db.WithContext(ctx).Where("sso_id = ?", ssoID).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.User{}, storage.ErrNotFound
	}
	return user, err
}

func (store *SQLite) HasUsers(ctx context.Context) (bool, error) {
	var count int64
	err := store.db.WithContext(ctx).Model(&models.User{}).Limit(1).Count(&count).Error
	return count > 0, err
}

func (store *SQLite) UpsertClientAuth(ctx context.Context, client models.Client) error {
	return store.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "uuid"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"token", "token_issued_at", "token_expires_at", "token_revoked_at", "updated_at",
		}),
	}).Create(&client).Error
}

func (store *SQLite) DeleteClientAuth(ctx context.Context, uuid string) error {
	return store.db.WithContext(ctx).Delete(&models.Client{}, "uuid = ?", uuid).Error
}

func (store *SQLite) UpsertSessionAuth(ctx context.Context, session models.Session) error {
	return store.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "session"}},
		DoUpdates: clause.AssignmentColumns([]string{"uuid", "session_digest", "expires", "created_at"}),
	}).Create(&session).Error
}

func (store *SQLite) DeleteSessionAuth(ctx context.Context, digest []byte) error {
	return store.db.WithContext(ctx).Delete(&models.Session{}, "session_digest = ?", digest).Error
}

func (store *SQLite) DeleteAllSessionsAuth(ctx context.Context) error {
	return store.db.WithContext(ctx).Where("1 = 1").Delete(&models.Session{}).Error
}

func (store *SQLite) DeleteSessionsByUserAuth(ctx context.Context, uuid string) error {
	return store.db.WithContext(ctx).Delete(&models.Session{}, "uuid = ?", uuid).Error
}

func (store *SQLite) DeleteExpiredSessionsAuth(ctx context.Context, before time.Time) error {
	return store.db.WithContext(ctx).Delete(&models.Session{}, "expires < ?", before).Error
}

func (store *SQLite) UpsertUserAuth(ctx context.Context, user models.User) error {
	return store.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "uuid"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"username", "passwd", "sso_type", "sso_id", "two_factor", "updated_at",
		}),
	}).Create(&user).Error
}

func (store *SQLite) DeleteUserAuth(ctx context.Context, uuid string) error {
	return store.db.WithContext(ctx).Delete(&models.User{}, "uuid = ?", uuid).Error
}

func (*SQLite) IsExternalControlStore() bool { return false }

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
