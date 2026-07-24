//go:build scale

package cmd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/komari-monitor/komari/database/controlstore"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/database/records"
	"github.com/komari-monitor/komari/database/telemetrystore"
	"github.com/komari-monitor/komari/database/telemetrywriter"
	"github.com/komari-monitor/komari/internal/credentialcache"
	"github.com/komari-monitor/komari/internal/storage"
	"gorm.io/gorm"
)

const storageStartupTimeout = 30 * time.Second

func installStorageAdapters() {
	db := dbcore.GetDBInstance()
	ctx, cancel := context.WithTimeout(context.Background(), storageStartupTimeout)
	defer cancel()
	if _, ok := storage.Control(); !ok {
		control, err := configuredControlStore(ctx, db)
		if err != nil {
			panic(fmt.Errorf("initialize control store: %w", err))
		}
		if _, err := storage.InstallControl(control); err != nil {
			panic(fmt.Errorf("install control store: %w", err))
		}
	}
	if _, ok := storage.Telemetry(); !ok {
		telemetry, err := configuredTelemetryStore(ctx, db)
		if err != nil {
			panic(fmt.Errorf("initialize telemetry store: %w", err))
		}
		if _, err := storage.InstallTelemetry(telemetry); err != nil {
			panic(fmt.Errorf("install telemetry store: %w", err))
		}
	}
}

func configuredControlStore(ctx context.Context, db *gorm.DB) (storage.ControlStore, error) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("KOMARI_CONTROL_STORE"))) {
	case "", "sqlite":
		return controlstore.NewSQLite(db)
	case "postgres", "postgresql":
		url := strings.TrimSpace(os.Getenv("KOMARI_POSTGRES_URL"))
		allowInsecure, err := envBool("KOMARI_POSTGRES_ALLOW_INSECURE", false)
		if err != nil {
			return nil, err
		}
		maxConns, err := envInt32("KOMARI_POSTGRES_MAX_CONNS", 32)
		if err != nil {
			return nil, err
		}
		minConns, err := envInt32("KOMARI_POSTGRES_MIN_CONNS", 2)
		if err != nil {
			return nil, err
		}
		store, err := controlstore.NewPostgres(ctx, controlstore.PostgresConfig{
			URL: url, RequireTLS: !allowInsecure, MaxConns: maxConns, MinConns: minConns,
		})
		if err != nil {
			return nil, err
		}
		if err := store.Migrate(ctx); err != nil {
			_ = store.Close(ctx)
			return nil, fmt.Errorf("migrate PostgreSQL control schema: %w", err)
		}
		if err := bootstrapPostgresControl(ctx, db, store); err != nil {
			_ = store.Close(ctx)
			return nil, err
		}
		return store, nil
	default:
		return nil, errors.New("KOMARI_CONTROL_STORE must be sqlite or postgres")
	}
}

func bootstrapPostgresControl(ctx context.Context, db *gorm.DB, store *controlstore.Postgres) error {
	empty, err := store.IsEmpty(ctx)
	if err != nil || !empty {
		return err
	}
	var users []models.User
	if err := db.WithContext(ctx).Find(&users).Error; err != nil {
		return err
	}
	if len(users) == 0 {
		return nil
	}
	enabled, err := envBool("KOMARI_POSTGRES_BOOTSTRAP_FROM_SQLITE", false)
	if err != nil {
		return err
	}
	if !enabled {
		return errors.New("PostgreSQL control store is empty while SQLite contains users; set KOMARI_POSTGRES_BOOTSTRAP_FROM_SQLITE=true for the one-time migration")
	}
	var clients []models.Client
	if err := db.WithContext(ctx).Where("token <> ?", "").Find(&clients).Error; err != nil {
		return err
	}
	var sessions []models.Session
	if err := db.WithContext(ctx).Find(&sessions).Error; err != nil {
		return err
	}
	for index := range sessions {
		if len(sessions[index].SessionDigest) == 0 && sessions[index].Session != "" {
			digest := credentialcache.Digest(sessions[index].Session)
			sessions[index].SessionDigest = append([]byte(nil), digest[:]...)
		}
		// Do not retain a second plaintext copy in the scale control store.
		sessions[index].Session = ""
	}
	return store.Bootstrap(ctx, users, clients, sessions)
}

func configuredTelemetryStore(ctx context.Context, db *gorm.DB) (storage.TelemetryStore, error) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("KOMARI_TELEMETRY_STORE"))) {
	case "", "sqlite":
		writerDB, err := dbcore.GetWriterDBInstance()
		if err != nil {
			return nil, err
		}
		return records.NewSQLiteTelemetryStore(db, writerDB, telemetrywriter.Config{})
	case "clickhouse":
		addresses := splitNonEmpty(os.Getenv("KOMARI_CLICKHOUSE_ADDRS"))
		allowInsecure, err := envBool("KOMARI_CLICKHOUSE_ALLOW_INSECURE", false)
		if err != nil {
			return nil, err
		}
		var tlsConfig *tls.Config
		if !allowInsecure {
			tlsConfig, err = clickHouseTLSConfig()
			if err != nil {
				return nil, err
			}
		}
		maxConns, err := envInt("KOMARI_CLICKHOUSE_MAX_CONNS", 32)
		if err != nil {
			return nil, err
		}
		idleConns, err := envInt("KOMARI_CLICKHOUSE_IDLE_CONNS", 8)
		if err != nil {
			return nil, err
		}
		store, err := telemetrystore.NewClickHouse(ctx, telemetrystore.ClickHouseConfig{
			Addresses:   addresses,
			Database:    strings.TrimSpace(os.Getenv("KOMARI_CLICKHOUSE_DATABASE")),
			Username:    strings.TrimSpace(os.Getenv("KOMARI_CLICKHOUSE_USERNAME")),
			Password:    os.Getenv("KOMARI_CLICKHOUSE_PASSWORD"),
			TablePrefix: strings.TrimSpace(os.Getenv("KOMARI_CLICKHOUSE_TABLE_PREFIX")),
			RequireTLS:  !allowInsecure, TLSConfig: tlsConfig,
			MaxOpenConns: maxConns, MaxIdleConns: idleConns,
			FreeBufOnConnRelease: true,
		})
		if err != nil {
			return nil, err
		}
		if err := store.Migrate(ctx); err != nil {
			_ = store.Close(ctx)
			return nil, fmt.Errorf("migrate ClickHouse telemetry schema: %w", err)
		}
		return store, nil
	default:
		return nil, errors.New("KOMARI_TELEMETRY_STORE must be sqlite or clickhouse")
	}
}

func clickHouseTLSConfig() (*tls.Config, error) {
	config := &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: strings.TrimSpace(os.Getenv("KOMARI_CLICKHOUSE_TLS_SERVER_NAME")),
	}
	if caPath := strings.TrimSpace(os.Getenv("KOMARI_CLICKHOUSE_TLS_CA")); caPath != "" {
		pem, err := os.ReadFile(caPath)
		if err != nil {
			return nil, fmt.Errorf("read ClickHouse CA: %w", err)
		}
		roots, err := x509.SystemCertPool()
		if err != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, errors.New("ClickHouse CA file contains no valid certificates")
		}
		config.RootCAs = roots
	}
	certPath := strings.TrimSpace(os.Getenv("KOMARI_CLICKHOUSE_TLS_CERT"))
	keyPath := strings.TrimSpace(os.Getenv("KOMARI_CLICKHOUSE_TLS_KEY"))
	if (certPath == "") != (keyPath == "") {
		return nil, errors.New("ClickHouse client certificate and key must be configured together")
	}
	if certPath != "" {
		certificate, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return nil, fmt.Errorf("load ClickHouse client certificate: %w", err)
		}
		config.Certificates = []tls.Certificate{certificate}
	}
	return config, nil
}

func splitNonEmpty(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func envBool(key string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean", key)
	}
	return value, nil
}

func envInt(key string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", key)
	}
	return value, nil
}

func envInt32(key string, fallback int32) (int32, error) {
	value, err := envInt(key, int(fallback))
	if err != nil {
		return 0, err
	}
	if value < 0 || int64(value) > int64(^uint32(0)>>1) {
		return 0, fmt.Errorf("%s is outside the int32 range", key)
	}
	return int32(value), nil
}
