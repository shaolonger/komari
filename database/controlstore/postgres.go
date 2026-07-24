//go:build scale

package controlstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/internal/storage"
)

const postgresSchemaVersion = 1

type PostgresConfig struct {
	URL                   string
	RequireTLS            bool
	MaxConns              int32
	MinConns              int32
	MaxConnLifetime       time.Duration
	MaxConnLifetimeJitter time.Duration
	MaxConnIdleTime       time.Duration
	HealthCheckPeriod     time.Duration
	ConnectTimeout        time.Duration
}

type Postgres struct {
	pool *pgxpool.Pool
}

var (
	_ storage.ControlStore               = (*Postgres)(nil)
	_ storage.ControlStateWriter         = (*Postgres)(nil)
	_ storage.ControlBootstrapper        = (*Postgres)(nil)
	_ storage.ExternalControlStateWriter = (*Postgres)(nil)
)

func NewPostgres(ctx context.Context, config PostgresConfig) (*Postgres, error) {
	poolConfig, err := postgresPoolConfig(config)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, errors.New("open PostgreSQL control pool")
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping PostgreSQL control store: %w", err)
	}
	return &Postgres{pool: pool}, nil
}

func postgresPoolConfig(config PostgresConfig) (*pgxpool.Config, error) {
	if config.URL == "" {
		return nil, errors.New("PostgreSQL URL is required")
	}
	poolConfig, err := pgxpool.ParseConfig(config.URL)
	if err != nil {
		return nil, errors.New("parse PostgreSQL connection configuration")
	}
	if config.RequireTLS {
		if poolConfig.ConnConfig.TLSConfig == nil {
			return nil, errors.New("PostgreSQL TLS is required")
		}
		for _, fallback := range poolConfig.ConnConfig.Fallbacks {
			if fallback.TLSConfig == nil {
				return nil, errors.New("PostgreSQL configuration permits a plaintext fallback")
			}
		}
	}
	if config.MaxConns == 0 {
		config.MaxConns = 32
	}
	if config.MaxConns < 1 || config.MaxConns > 256 {
		return nil, errors.New("PostgreSQL max connections must be between 1 and 256")
	}
	if config.MinConns < 0 || config.MinConns > config.MaxConns {
		return nil, errors.New("PostgreSQL min connections must be between zero and max connections")
	}
	if config.MaxConnLifetime <= 0 {
		config.MaxConnLifetime = time.Hour
	}
	if config.MaxConnLifetimeJitter <= 0 {
		config.MaxConnLifetimeJitter = 5 * time.Minute
	}
	if config.MaxConnIdleTime <= 0 {
		config.MaxConnIdleTime = 15 * time.Minute
	}
	if config.HealthCheckPeriod <= 0 {
		config.HealthCheckPeriod = 30 * time.Second
	}
	if config.ConnectTimeout <= 0 {
		config.ConnectTimeout = 10 * time.Second
	}
	poolConfig.MaxConns = config.MaxConns
	poolConfig.MinConns = config.MinConns
	poolConfig.MaxConnLifetime = config.MaxConnLifetime
	poolConfig.MaxConnLifetimeJitter = config.MaxConnLifetimeJitter
	poolConfig.MaxConnIdleTime = config.MaxConnIdleTime
	poolConfig.HealthCheckPeriod = config.HealthCheckPeriod
	poolConfig.ConnConfig.ConnectTimeout = config.ConnectTimeout
	poolConfig.ConnConfig.RuntimeParams["application_name"] = "komari-control"
	return poolConfig, nil
}

func (store *Postgres) Migrate(ctx context.Context) error {
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(715984231)`); err != nil {
		return err
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS komari_schema_migrations (
			component TEXT PRIMARY KEY,
			version INTEGER NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS komari_users (
			uuid TEXT PRIMARY KEY,
			username TEXT NOT NULL UNIQUE,
			passwd TEXT NOT NULL,
			sso_type TEXT NOT NULL DEFAULT '',
			sso_id TEXT NOT NULL DEFAULT '',
			two_factor TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ,
			updated_at TIMESTAMPTZ
		)`,
		`CREATE INDEX IF NOT EXISTS idx_komari_users_sso_id ON komari_users (sso_id) WHERE sso_id <> ''`,
		`CREATE TABLE IF NOT EXISTS komari_clients (
			uuid TEXT PRIMARY KEY,
			token TEXT NOT NULL UNIQUE,
			token_issued_at TIMESTAMPTZ,
			token_expires_at TIMESTAMPTZ,
			token_revoked_at TIMESTAMPTZ,
			created_at TIMESTAMPTZ,
			updated_at TIMESTAMPTZ
		)`,
		`CREATE TABLE IF NOT EXISTS komari_sessions (
			session_digest BYTEA PRIMARY KEY,
			uuid TEXT NOT NULL REFERENCES komari_users(uuid) ON DELETE CASCADE,
			expires TIMESTAMPTZ NOT NULL,
			created_at TIMESTAMPTZ
		)`,
		`CREATE INDEX IF NOT EXISTS idx_komari_sessions_uuid ON komari_sessions (uuid)`,
		`CREATE INDEX IF NOT EXISTS idx_komari_sessions_expires ON komari_sessions (expires)`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(ctx, statement); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO komari_schema_migrations(component, version)
		VALUES ('control', $1)
		ON CONFLICT(component) DO UPDATE SET version=EXCLUDED.version, applied_at=now()
	`, postgresSchemaVersion); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (store *Postgres) ClientCredential(ctx context.Context, token string) (models.Client, error) {
	if token == "" {
		return models.Client{}, storage.ErrNotFound
	}
	return scanPostgresClient(store.pool.QueryRow(ctx, `
		SELECT uuid, token, token_issued_at, token_expires_at, token_revoked_at, created_at, updated_at
		FROM komari_clients WHERE token=$1
	`, token))
}

func (store *Postgres) ClientByUUID(ctx context.Context, uuid string) (models.Client, error) {
	if uuid == "" {
		return models.Client{}, storage.ErrNotFound
	}
	return scanPostgresClient(store.pool.QueryRow(ctx, `
		SELECT uuid, token, token_issued_at, token_expires_at, token_revoked_at, created_at, updated_at
		FROM komari_clients WHERE uuid=$1
	`, uuid))
}

func scanPostgresClient(row pgx.Row) (models.Client, error) {
	var client models.Client
	var issued, expires, revoked, created, updated pgtype.Timestamptz
	err := row.Scan(&client.UUID, &client.Token, &issued, &expires, &revoked, &created, &updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return models.Client{}, storage.ErrNotFound
	}
	if err != nil {
		return models.Client{}, err
	}
	client.TokenIssuedAt = localTimeFromPostgres(issued)
	client.TokenExpiresAt = localTimeFromPostgres(expires)
	client.TokenRevokedAt = localTimeFromPostgres(revoked)
	client.CreatedAt = localTimeFromPostgres(created)
	client.UpdatedAt = localTimeFromPostgres(updated)
	return client, nil
}

func (store *Postgres) SessionCredential(ctx context.Context, _ string, digest []byte) (models.Session, error) {
	if len(digest) == 0 {
		return models.Session{}, storage.ErrNotFound
	}
	var session models.Session
	var expires, created pgtype.Timestamptz
	err := store.pool.QueryRow(ctx, `
		SELECT uuid, session_digest, expires, created_at
		FROM komari_sessions WHERE session_digest=$1
	`, digest).Scan(&session.UUID, &session.SessionDigest, &expires, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		return models.Session{}, storage.ErrNotFound
	}
	if err != nil {
		return models.Session{}, err
	}
	session.Expires = localTimeFromPostgres(expires)
	session.CreatedAt = localTimeFromPostgres(created)
	return session, nil
}

func (store *Postgres) UserByUUID(ctx context.Context, uuid string) (models.User, error) {
	return store.user(ctx, "uuid", uuid)
}

func (store *Postgres) UserByUsername(ctx context.Context, username string) (models.User, error) {
	return store.user(ctx, "username", username)
}

func (store *Postgres) UserBySSO(ctx context.Context, ssoID string) (models.User, error) {
	return store.user(ctx, "sso_id", ssoID)
}

func (store *Postgres) user(ctx context.Context, field, value string) (models.User, error) {
	if value == "" {
		return models.User{}, storage.ErrNotFound
	}
	query := `SELECT uuid, username, passwd, sso_type, sso_id, two_factor, created_at, updated_at
		FROM komari_users WHERE ` + field + `=$1`
	var user models.User
	var created, updated pgtype.Timestamptz
	err := store.pool.QueryRow(ctx, query, value).Scan(
		&user.UUID, &user.Username, &user.Passwd, &user.SSOType, &user.SSOID,
		&user.TwoFactor, &created, &updated,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return models.User{}, storage.ErrNotFound
	}
	if err != nil {
		return models.User{}, err
	}
	user.CreatedAt = localTimeFromPostgres(created)
	user.UpdatedAt = localTimeFromPostgres(updated)
	return user, nil
}

func (store *Postgres) HasUsers(ctx context.Context) (bool, error) {
	var exists bool
	err := store.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM komari_users LIMIT 1)`).Scan(&exists)
	return exists, err
}

func (store *Postgres) UpsertClientAuth(ctx context.Context, client models.Client) error {
	if client.UUID == "" || client.Token == "" {
		return errors.New("client UUID and token are required")
	}
	_, err := store.pool.Exec(ctx, `
		INSERT INTO komari_clients(
			uuid, token, token_issued_at, token_expires_at, token_revoked_at, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT(uuid) DO UPDATE SET
			token=EXCLUDED.token,
			token_issued_at=EXCLUDED.token_issued_at,
			token_expires_at=EXCLUDED.token_expires_at,
			token_revoked_at=EXCLUDED.token_revoked_at,
			updated_at=EXCLUDED.updated_at
	`, client.UUID, client.Token, nullablePostgresTime(client.TokenIssuedAt),
		nullablePostgresTime(client.TokenExpiresAt), nullablePostgresTime(client.TokenRevokedAt),
		nullablePostgresTime(client.CreatedAt), nullablePostgresTime(client.UpdatedAt))
	return err
}

func (store *Postgres) DeleteClientAuth(ctx context.Context, uuid string) error {
	_, err := store.pool.Exec(ctx, `DELETE FROM komari_clients WHERE uuid=$1`, uuid)
	return err
}

func (store *Postgres) UpsertSessionAuth(ctx context.Context, session models.Session) error {
	if session.UUID == "" || len(session.SessionDigest) == 0 || session.Expires.ToTime().IsZero() {
		return errors.New("session UUID, digest and expiry are required")
	}
	_, err := store.pool.Exec(ctx, `
		INSERT INTO komari_sessions(session_digest, uuid, expires, created_at)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT(session_digest) DO UPDATE SET
			uuid=EXCLUDED.uuid, expires=EXCLUDED.expires, created_at=EXCLUDED.created_at
	`, session.SessionDigest, session.UUID, session.Expires.ToTime(), nullablePostgresTime(session.CreatedAt))
	return err
}

func (store *Postgres) DeleteSessionAuth(ctx context.Context, digest []byte) error {
	_, err := store.pool.Exec(ctx, `DELETE FROM komari_sessions WHERE session_digest=$1`, digest)
	return err
}

func (store *Postgres) DeleteAllSessionsAuth(ctx context.Context) error {
	_, err := store.pool.Exec(ctx, `DELETE FROM komari_sessions`)
	return err
}

func (store *Postgres) DeleteSessionsByUserAuth(ctx context.Context, uuid string) error {
	_, err := store.pool.Exec(ctx, `DELETE FROM komari_sessions WHERE uuid=$1`, uuid)
	return err
}

func (store *Postgres) DeleteExpiredSessionsAuth(ctx context.Context, before time.Time) error {
	_, err := store.pool.Exec(ctx, `DELETE FROM komari_sessions WHERE expires < $1`, before)
	return err
}

func (store *Postgres) UpsertUserAuth(ctx context.Context, user models.User) error {
	if user.UUID == "" || user.Username == "" || user.Passwd == "" {
		return errors.New("user UUID, username and password hash are required")
	}
	_, err := store.pool.Exec(ctx, `
		INSERT INTO komari_users(
			uuid, username, passwd, sso_type, sso_id, two_factor, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT(uuid) DO UPDATE SET
			username=EXCLUDED.username,
			passwd=EXCLUDED.passwd,
			sso_type=EXCLUDED.sso_type,
			sso_id=EXCLUDED.sso_id,
			two_factor=EXCLUDED.two_factor,
			updated_at=EXCLUDED.updated_at
	`, user.UUID, user.Username, user.Passwd, user.SSOType, user.SSOID, user.TwoFactor,
		nullablePostgresTime(user.CreatedAt), nullablePostgresTime(user.UpdatedAt))
	return err
}

func (store *Postgres) DeleteUserAuth(ctx context.Context, uuid string) error {
	_, err := store.pool.Exec(ctx, `DELETE FROM komari_users WHERE uuid=$1`, uuid)
	return err
}

func (*Postgres) IsExternalControlStore() bool { return true }

func (store *Postgres) IsEmpty(ctx context.Context) (bool, error) {
	exists, err := store.HasUsers(ctx)
	return !exists, err
}

func (store *Postgres) Bootstrap(ctx context.Context, users []models.User, clients []models.Client, sessions []models.Session) error {
	empty, err := store.IsEmpty(ctx)
	if err != nil {
		return err
	}
	if !empty {
		return errors.New("refusing to bootstrap a non-empty PostgreSQL control store")
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, user := range users {
		if _, err := tx.Exec(ctx, `
			INSERT INTO komari_users(uuid,username,passwd,sso_type,sso_id,two_factor,created_at,updated_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8)
		`, user.UUID, user.Username, user.Passwd, user.SSOType, user.SSOID, user.TwoFactor,
			nullablePostgresTime(user.CreatedAt), nullablePostgresTime(user.UpdatedAt)); err != nil {
			return err
		}
	}
	for _, client := range clients {
		if _, err := tx.Exec(ctx, `
			INSERT INTO komari_clients(uuid,token,token_issued_at,token_expires_at,token_revoked_at,created_at,updated_at)
			VALUES($1,$2,$3,$4,$5,$6,$7)
		`, client.UUID, client.Token, nullablePostgresTime(client.TokenIssuedAt),
			nullablePostgresTime(client.TokenExpiresAt), nullablePostgresTime(client.TokenRevokedAt),
			nullablePostgresTime(client.CreatedAt), nullablePostgresTime(client.UpdatedAt)); err != nil {
			return err
		}
	}
	for _, session := range sessions {
		if len(session.SessionDigest) == 0 {
			continue
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO komari_sessions(session_digest,uuid,expires,created_at)
			VALUES($1,$2,$3,$4)
		`, session.SessionDigest, session.UUID, session.Expires.ToTime(), nullablePostgresTime(session.CreatedAt)); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (store *Postgres) Health(ctx context.Context) (storage.Health, error) {
	started := time.Now()
	health := storage.Health{Backend: "postgres", CheckedAt: started}
	var value int
	err := store.pool.QueryRow(ctx, `SELECT 1`).Scan(&value)
	health.Latency = time.Since(started)
	health.Ready = err == nil && value == 1
	if err != nil {
		return health, err
	}
	if !health.Ready {
		return health, errors.New("PostgreSQL control health query returned an unexpected value")
	}
	return health, nil
}

func (store *Postgres) Close(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		store.pool.Close()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func nullablePostgresTime(value models.LocalTime) any {
	at := value.ToTime()
	if at.IsZero() {
		return nil
	}
	return at
}

func localTimeFromPostgres(value pgtype.Timestamptz) models.LocalTime {
	if !value.Valid {
		return models.LocalTime(time.Time{})
	}
	return models.FromTime(value.Time)
}
