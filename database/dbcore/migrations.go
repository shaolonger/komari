package dbcore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"regexp"
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
	{
		version: 2,
		name:    "incremental_telemetry_compaction",
		statements: []string{
			"DELETE FROM records_long_term WHERE rowid NOT IN (SELECT MAX(rowid) FROM records_long_term GROUP BY client, time)",
			"DELETE FROM gpu_records_long_term WHERE rowid NOT IN (SELECT MAX(rowid) FROM gpu_records_long_term GROUP BY client, time, device_index)",
			"CREATE UNIQUE INDEX IF NOT EXISTS idx_record_lt_bucket ON records_long_term(client, time)",
			"CREATE UNIQUE INDEX IF NOT EXISTS idx_gpu_record_lt_bucket ON gpu_records_long_term(client, time, device_index)",
			`CREATE TABLE IF NOT EXISTS telemetry_compaction_state (
				stream TEXT PRIMARY KEY NOT NULL,
				watermark DATETIME NOT NULL,
				updated_at DATETIME NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS telemetry_compaction_pending (
				stream TEXT NOT NULL,
				bucket DATETIME NOT NULL,
				bucket_end DATETIME NOT NULL,
				max_row_id INTEGER NOT NULL,
				PRIMARY KEY(stream, bucket)
			)`,
		},
	},
	{
		version: 3,
		name:    "hourly_telemetry_rollups",
		statements: []string{
			"CREATE TABLE IF NOT EXISTS records_hourly AS SELECT * FROM records WHERE 0",
			"CREATE TABLE IF NOT EXISTS gpu_records_hourly AS SELECT * FROM gpu_records WHERE 0",
			"CREATE UNIQUE INDEX IF NOT EXISTS idx_records_hourly_bucket ON records_hourly(client, time)",
			"CREATE INDEX IF NOT EXISTS idx_records_hourly_time ON records_hourly(time)",
			"CREATE UNIQUE INDEX IF NOT EXISTS idx_gpu_records_hourly_bucket ON gpu_records_hourly(client, time, device_index)",
			"CREATE INDEX IF NOT EXISTS idx_gpu_records_hourly_time ON gpu_records_hourly(time)",
			`CREATE TABLE IF NOT EXISTS record_rollup_summaries (
				client varchar(36) NOT NULL,
				time DATETIME NOT NULL,
				resolution_seconds INTEGER NOT NULL,
				sample_count INTEGER NOT NULL,
				first_time DATETIME NOT NULL,
				last_time DATETIME NOT NULL,
				first_net_total_up INTEGER,
				last_net_total_up INTEGER,
				first_net_total_down INTEGER,
				last_net_total_down INTEGER,
				first_net_in INTEGER,
				last_net_in INTEGER,
				first_net_out INTEGER,
				last_net_out INTEGER,
				traffic_up INTEGER,
				traffic_down INTEGER,
				counter_resets INTEGER,
				cpu_peak decimal(5,2),
				gpu_peak decimal(5,2),
				load_peak decimal(5,2),
				temperature_peak decimal(5,2),
				PRIMARY KEY(client, time, resolution_seconds)
			)`,
			"CREATE INDEX IF NOT EXISTS idx_rollup_summary_resolution_time ON record_rollup_summaries(resolution_seconds, time)",
		},
	},
	{
		version: 4,
		name:    "client_notification_cascade",
		statements: []string{
			"-- rebuild per-client notification foreign keys with cascading deletes",
		},
		up: migrateClientNotificationCascade,
	},
	{
		version: 5,
		name:    "tiered_ping_rollups",
		statements: []string{
			`CREATE TABLE IF NOT EXISTS ping_rollups (
				client varchar(36) NOT NULL,
				task_id INTEGER NOT NULL,
				resolution_seconds INTEGER NOT NULL,
				bucket_time DATETIME NOT NULL,
				sample_count INTEGER NOT NULL,
				valid_count INTEGER NOT NULL,
				loss_count INTEGER NOT NULL,
				sum_value INTEGER NOT NULL,
				min_value INTEGER NOT NULL,
				max_value INTEGER NOT NULL,
				last_value INTEGER NOT NULL,
				last_time DATETIME NOT NULL,
				PRIMARY KEY(client, task_id, resolution_seconds, bucket_time),
				FOREIGN KEY(client) REFERENCES clients(uuid) ON DELETE CASCADE ON UPDATE CASCADE,
				FOREIGN KEY(task_id) REFERENCES ping_tasks(id) ON DELETE CASCADE ON UPDATE CASCADE
			)`,
			"CREATE INDEX IF NOT EXISTS idx_ping_rollup_resolution_time_task_client ON ping_rollups(resolution_seconds, bucket_time, task_id, client)",
		},
	},
}

var clientNotificationForeignKeyPattern = regexp.MustCompile(
	"(?i)(FOREIGN\\s+KEY\\s*\\(\\s*[`\"]?client[`\"]?\\s*\\)\\s*" +
		"REFERENCES\\s*[`\"]?clients[`\"]?\\s*\\(\\s*[`\"]?uuid[`\"]?\\s*\\))" +
		"(?:\\s+ON\\s+DELETE\\s+(?:NO\\s+ACTION|RESTRICT|SET\\s+NULL|SET\\s+DEFAULT|CASCADE))?" +
		"(?:\\s+ON\\s+UPDATE\\s+(?:NO\\s+ACTION|RESTRICT|SET\\s+NULL|SET\\s+DEFAULT|CASCADE))?",
)

type sqliteSchemaObject struct {
	Name string
	SQL  string
}

func quoteSQLiteIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

// rebuildClientNotificationCascade preserves the complete installed table
// definition, including legacy columns, while replacing only the client
// foreign-key action. SQLite cannot alter a foreign key in place.
func rebuildClientNotificationCascade(db *gorm.DB, table string) error {
	var createSQL string
	result := db.Raw(
		"SELECT sql FROM sqlite_master WHERE type='table' AND name=?",
		table,
	).Scan(&createSQL)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 || strings.TrimSpace(createSQL) == "" {
		return nil
	}

	matches := clientNotificationForeignKeyPattern.FindAllStringIndex(createSQL, -1)
	if len(matches) != 1 {
		return fmt.Errorf("table %s has %d client foreign keys, want 1", table, len(matches))
	}
	cascadeSQL := clientNotificationForeignKeyPattern.ReplaceAllString(
		createSQL,
		"$1 ON DELETE CASCADE ON UPDATE CASCADE",
	)

	var schemaObjects []sqliteSchemaObject
	if err := db.Raw(
		`SELECT name, sql
		 FROM sqlite_master
		 WHERE tbl_name=? AND type IN ('index','trigger') AND sql IS NOT NULL
		 ORDER BY type, name`,
		table,
	).Scan(&schemaObjects).Error; err != nil {
		return err
	}

	backupTable := table + "__cascade_v4_old"
	var backupCount int64
	if err := db.Raw(
		"SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?",
		backupTable,
	).Scan(&backupCount).Error; err != nil {
		return err
	}
	if backupCount != 0 {
		return fmt.Errorf("temporary migration table %s already exists", backupTable)
	}

	quotedTable := quoteSQLiteIdentifier(table)
	quotedBackup := quoteSQLiteIdentifier(backupTable)
	if err := db.Exec("ALTER TABLE " + quotedTable + " RENAME TO " + quotedBackup).Error; err != nil {
		return err
	}
	if err := db.Exec(cascadeSQL).Error; err != nil {
		return err
	}
	if err := db.Exec("INSERT INTO " + quotedTable + " SELECT * FROM " + quotedBackup).Error; err != nil {
		return err
	}
	if err := db.Exec("DROP TABLE " + quotedBackup).Error; err != nil {
		return err
	}
	for _, object := range schemaObjects {
		if strings.TrimSpace(object.SQL) == "" {
			continue
		}
		if err := db.Exec(object.SQL).Error; err != nil {
			return fmt.Errorf("restore schema object %s: %w", object.Name, err)
		}
	}
	return nil
}

// removeClientNotificationOrphans repairs rows that could be created by
// releases which declared notification foreign keys but did not enable SQLite
// foreign-key enforcement. The rows cannot represent a usable notification
// because their parent client no longer exists. Keeping the cleanup in the
// migration transaction guarantees it is rolled back if the table rebuild
// fails for any later reason.
func removeClientNotificationOrphans(db *gorm.DB, table string) (int64, error) {
	quotedTable := quoteSQLiteIdentifier(table)
	result := db.Exec(
		"DELETE FROM " + quotedTable +
			" WHERE NOT EXISTS (" +
			"SELECT 1 FROM clients WHERE clients.uuid = " + quotedTable + ".client" +
			")",
	)
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}

func migrateClientNotificationCascade(db *gorm.DB) error {
	for _, table := range []string{"offline_notifications", "traffic_report_notifications"} {
		var tableCount int64
		if err := db.Raw(
			"SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?",
			table,
		).Scan(&tableCount).Error; err != nil {
			return err
		}
		if tableCount == 0 {
			continue
		}
		removed, err := removeClientNotificationOrphans(db, table)
		if err != nil {
			return fmt.Errorf("repair %s orphan rows: %w", table, err)
		}
		if removed != 0 {
			log.Printf(
				"[migration 4] removed %d orphan rows from %s before enabling cascading foreign keys",
				removed,
				table,
			)
		}
		if err := rebuildClientNotificationCascade(db, table); err != nil {
			return fmt.Errorf("migrate %s cascade: %w", table, err)
		}
		var violations int64
		if err := db.Raw(
			"SELECT count(*) FROM pragma_foreign_key_check(?)",
			table,
		).Scan(&violations).Error; err != nil {
			return err
		}
		if violations != 0 {
			return fmt.Errorf("%s foreign key check found %d violations", table, violations)
		}
	}
	return nil
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
