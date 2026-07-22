package dbcore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

const schemaMigrationTable = "schema_migrations"

type migration struct {
	version    int
	name       string
	statements []string
	up         func(*gorm.DB) error
}

type appliedMigration struct {
	Version  int
	Name     string
	Checksum string
	Applied  string
}

func (m migration) checksum() string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s\x00%s", m.version, m.name, strings.Join(m.statements, "\x00"))))
	return hex.EncodeToString(sum[:])
}

var schemaMigrations = []migration{
	{
		version: 1,
		name:    "sqlite_hot_path_indexes",
		statements: []string{
			"CREATE INDEX IF NOT EXISTS idx_record_client_time ON records(client, time)",
			"CREATE INDEX IF NOT EXISTS idx_records_time ON records(time)",
			"CREATE INDEX IF NOT EXISTS idx_record_lt_client_time ON records_long_term(client, time)",
			"CREATE INDEX IF NOT EXISTS idx_record_lt_time ON records_long_term(time)",
			"CREATE INDEX IF NOT EXISTS idx_gpu_record_client_time_device ON gpu_records(client, time, device_index)",
			"CREATE INDEX IF NOT EXISTS idx_gpu_records_time ON gpu_records(time)",
			"CREATE INDEX IF NOT EXISTS idx_gpu_record_lt_client_time_device ON gpu_records_long_term(client, time, device_index)",
			"CREATE INDEX IF NOT EXISTS idx_gpu_record_lt_time ON gpu_records_long_term(time)",
			"CREATE INDEX IF NOT EXISTS idx_ping_record_client_task_time ON ping_records(client, task_id, time)",
			"CREATE INDEX IF NOT EXISTS idx_ping_record_task_time_client ON ping_records(task_id, time, client)",
			"CREATE INDEX IF NOT EXISTS idx_ping_records_time ON ping_records(time)",
			"CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_digest ON sessions(session_digest)",
			"CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires)",
			"CREATE INDEX IF NOT EXISTS idx_sessions_uuid ON sessions(uuid)",
		},
	},
}

func ensureMigrationTable(db *gorm.DB) error {
	return db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY NOT NULL,
		name TEXT NOT NULL,
		checksum TEXT NOT NULL,
		applied_at TEXT NOT NULL
	)`).Error
}

func currentUserVersion(db *gorm.DB) (int, error) {
	var version int
	if err := db.Raw("PRAGMA user_version").Scan(&version).Error; err != nil {
		return 0, err
	}
	return version, nil
}

func RunMigrations(ctx context.Context, db *gorm.DB) error {
	return runMigrations(ctx, db, schemaMigrations)
}

func runMigrations(ctx context.Context, db *gorm.DB, migrations []migration) error {
	if db == nil {
		return fmt.Errorf("migration database is required")
	}
	db = db.WithContext(ctx)
	if err := ensureMigrationTable(db); err != nil {
		return err
	}
	var applied []appliedMigration
	if err := db.Table(schemaMigrationTable).Order("version ASC").Find(&applied).Error; err != nil {
		return err
	}
	appliedByVersion := make(map[int]appliedMigration, len(applied))
	for _, record := range applied {
		if _, duplicate := appliedByVersion[record.Version]; duplicate {
			return fmt.Errorf("duplicate schema migration version %d", record.Version)
		}
		appliedByVersion[record.Version] = record
	}
	latestKnown := 0
	if len(migrations) > 0 {
		latestKnown = migrations[len(migrations)-1].version
	}
	for version := range appliedByVersion {
		if version > latestKnown {
			return fmt.Errorf("database contains unknown schema migration version %d", version)
		}
	}
	userVersion, err := currentUserVersion(db)
	if err != nil {
		return err
	}
	if userVersion > latestKnown {
		return fmt.Errorf("database schema version %d is newer than supported version %d", userVersion, latestKnown)
	}
	for index, item := range migrations {
		if item.version <= 0 || item.name == "" {
			return fmt.Errorf("invalid migration at index %d", index)
		}
		if index > 0 && migrations[index-1].version >= item.version {
			return fmt.Errorf("schema migrations are not strictly ordered at version %d", item.version)
		}
		checksum := item.checksum()
		if record, exists := appliedByVersion[item.version]; exists {
			if record.Name != item.name || record.Checksum != checksum {
				return fmt.Errorf("schema migration %d checksum/name mismatch", item.version)
			}
			continue
		}
		if err := db.Transaction(func(tx *gorm.DB) error {
			for _, statement := range item.statements {
				if err := tx.Exec(statement).Error; err != nil {
					return fmt.Errorf("migration %d (%s): %w", item.version, item.name, err)
				}
			}
			if item.up != nil {
				if err := item.up(tx); err != nil {
					return fmt.Errorf("migration %d (%s): %w", item.version, item.name, err)
				}
			}
			if err := tx.Exec(
				"INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)",
				item.version, item.name, checksum, time.Now().UTC().Format(time.RFC3339Nano),
			).Error; err != nil {
				return err
			}
			return tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", item.version)).Error
		}); err != nil {
			return err
		}
		appliedByVersion[item.version] = appliedMigration{Version: item.version, Name: item.name, Checksum: checksum}
		userVersion = item.version
	}
	if userVersion != latestKnown {
		if err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", latestKnown)).Error; err != nil {
			return err
		}
	}
	return nil
}

func CurrentSchemaVersion(ctx context.Context, db *gorm.DB) (int, error) {
	if db == nil {
		return 0, fmt.Errorf("migration database is required")
	}
	return currentUserVersion(db.WithContext(ctx))
}
