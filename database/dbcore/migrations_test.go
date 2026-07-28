package dbcore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/komari-monitor/komari/database/models"
	"gorm.io/gorm"
)

func createLegacyTelemetrySchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, statement := range []string{
		"CREATE TABLE records(client TEXT, time DATETIME, value INTEGER)",
		"CREATE TABLE records_long_term(client TEXT, time DATETIME, value INTEGER)",
		"CREATE TABLE gpu_records(client TEXT, time DATETIME, device_index INTEGER)",
		"CREATE TABLE gpu_records_long_term(client TEXT, time DATETIME, device_index INTEGER)",
		"CREATE TABLE ping_records(client TEXT, task_id INTEGER, time DATETIME, value INTEGER)",
		"CREATE TABLE sessions(session TEXT PRIMARY KEY, session_digest BLOB, expires DATETIME, uuid TEXT)",
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func TestSchemaMigrationUpgradesLegacyDatabaseIdempotently(t *testing.T) {
	db, _, _ := openSQLiteForTest(t)
	createLegacyTelemetrySchema(t, db)
	if err := db.Exec("INSERT INTO records(client,time,value) VALUES(?,?,?)", "node-a", "2026-01-01 00:00:00", 7).Error; err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"INSERT INTO records_long_term(client,time,value) VALUES('node-a','2026-01-01 00:00:00',1),('node-a','2026-01-01 00:00:00',2)",
		"INSERT INTO gpu_records_long_term(client,time,device_index) VALUES('node-a','2026-01-01 00:00:00',0),('node-a','2026-01-01 00:00:00',0)",
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	if err := RunMigrations(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := RunMigrations(ctx, db); err != nil {
		t.Fatalf("repeated migration failed: %v", err)
	}
	version, err := CurrentSchemaVersion(ctx, db)
	if err != nil || version != 4 {
		t.Fatalf("schema version=%d err=%v, want 4", version, err)
	}
	var migrationCount, dataCount int64
	if err := db.Table(schemaMigrationTable).Count(&migrationCount).Error; err != nil || migrationCount != 4 {
		t.Fatalf("migration rows=%d err=%v", migrationCount, err)
	}
	if err := db.Table("records").Count(&dataCount).Error; err != nil || dataCount != 1 {
		t.Fatalf("legacy data rows=%d err=%v", dataCount, err)
	}
	for _, table := range []string{"records_long_term", "gpu_records_long_term"} {
		if err := db.Table(table).Count(&dataCount).Error; err != nil || dataCount != 1 {
			t.Fatalf("deduplicated %s rows=%d err=%v, want 1", table, dataCount, err)
		}
	}
	for _, table := range []string{"telemetry_compaction_state", "telemetry_compaction_pending"} {
		var count int64
		if err := db.Raw("SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&count).Error; err != nil || count != 1 {
			t.Fatalf("table %s count=%d err=%v", table, count, err)
		}
	}

	for _, name := range []string{
		"idx_record_client_time", "idx_record_lt_client_time",
		"idx_gpu_record_client_time_device", "idx_gpu_record_lt_client_time_device",
		"idx_ping_record_client_task_time", "idx_ping_record_task_time_client",
		"idx_sessions_digest", "idx_sessions_expires", "idx_sessions_uuid",
		"idx_record_lt_bucket", "idx_gpu_record_lt_bucket",
		"idx_records_hourly_bucket", "idx_gpu_records_hourly_bucket", "idx_rollup_summary_resolution_time",
	} {
		var count int64
		if err := db.Raw("SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?", name).Scan(&count).Error; err != nil || count != 1 {
			t.Fatalf("index %s count=%d err=%v", name, count, err)
		}
	}
}

func TestClientNotificationCascadeMigrationPreservesSchemaAndData(t *testing.T) {
	db, primary, _ := openSQLiteForTest(t)
	// Releases before v1.3.0 did not enable foreign-key enforcement on every
	// SQLite connection, so production databases can legitimately contain
	// historical notification rows whose client has already been deleted.
	primary.SetMaxOpenConns(1)
	if err := db.Exec("PRAGMA foreign_keys = OFF").Error; err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE clients (
			uuid TEXT PRIMARY KEY,
			token TEXT NOT NULL UNIQUE
		)`,
		`CREATE TABLE offline_notifications (
			client TEXT NOT NULL UNIQUE,
			enable BOOLEAN DEFAULT false,
			grace_period INTEGER NOT NULL DEFAULT 180,
			last_notified DATETIME,
			legacy_note TEXT,
			CONSTRAINT fk_offline_notifications_client_info
				FOREIGN KEY (client) REFERENCES clients(uuid)
		)`,
		`CREATE INDEX idx_offline_notifications_client
			ON offline_notifications(client)`,
		`CREATE TRIGGER offline_notifications_audit
			AFTER UPDATE ON offline_notifications
			BEGIN
				SELECT NEW.client;
			END`,
		`CREATE TABLE traffic_report_notifications (
			client TEXT NOT NULL UNIQUE,
			enable BOOLEAN DEFAULT false,
			daily BOOLEAN DEFAULT false,
			weekly BOOLEAN DEFAULT false,
			monthly BOOLEAN DEFAULT false,
			last_daily_notified DATETIME,
			last_weekly_notified DATETIME,
			last_monthly_notified DATETIME,
			legacy_note TEXT,
			CONSTRAINT fk_traffic_report_notifications_client_info
				FOREIGN KEY (client) REFERENCES clients(uuid)
				ON DELETE NO ACTION ON UPDATE NO ACTION
		)`,
		`CREATE INDEX idx_traffic_report_notifications_client
			ON traffic_report_notifications(client)`,
		`INSERT INTO clients(uuid,token) VALUES('node-cascade','token-cascade')`,
		`INSERT INTO offline_notifications(client,enable,legacy_note)
			VALUES('node-cascade',true,'offline-preserved')`,
		`INSERT INTO traffic_report_notifications(client,enable,daily,legacy_note)
			VALUES('node-cascade',true,true,'traffic-preserved')`,
		`INSERT INTO offline_notifications(client,enable,legacy_note)
			VALUES('orphan-offline',true,'orphan-must-be-removed')`,
		`INSERT INTO traffic_report_notifications(client,enable,daily,legacy_note)
			VALUES('orphan-traffic',true,true,'orphan-must-be-removed')`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
		t.Fatal(err)
	}
	var initialViolations int64
	if err := db.Raw("SELECT count(*) FROM pragma_foreign_key_check").Scan(&initialViolations).Error; err != nil {
		t.Fatal(err)
	}
	if initialViolations != 2 {
		t.Fatalf("historical fixture violations=%d, want 2", initialViolations)
	}

	item := schemaMigrations[len(schemaMigrations)-1]
	if item.version != 4 {
		t.Fatalf("last migration version = %d, want 4", item.version)
	}
	if err := runMigrations(context.Background(), db, []migration{item}); err != nil {
		t.Fatal(err)
	}
	if err := runMigrations(context.Background(), db, []migration{item}); err != nil {
		t.Fatalf("repeated migration failed: %v", err)
	}

	for _, table := range []string{"offline_notifications", "traffic_report_notifications"} {
		var action struct {
			OnDelete string `gorm:"column:on_delete"`
			OnUpdate string `gorm:"column:on_update"`
		}
		if err := db.Raw(
			`SELECT on_delete, on_update
			 FROM pragma_foreign_key_list(?)
			 WHERE "table"='clients' AND "from"='client'`,
			table,
		).Scan(&action).Error; err != nil {
			t.Fatal(err)
		}
		if action.OnDelete != "CASCADE" || action.OnUpdate != "CASCADE" {
			t.Fatalf("%s foreign-key action = %+v, want CASCADE/CASCADE", table, action)
		}
		var indexCount int64
		if err := db.Raw(
			"SELECT count(*) FROM sqlite_master WHERE type='index' AND tbl_name=? AND sql IS NOT NULL",
			table,
		).Scan(&indexCount).Error; err != nil || indexCount != 1 {
			t.Fatalf("%s explicit indexes=%d err=%v, want 1", table, indexCount, err)
		}
	}
	var triggerCount int64
	if err := db.Raw(
		"SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name='offline_notifications_audit'",
	).Scan(&triggerCount).Error; err != nil || triggerCount != 1 {
		t.Fatalf("preserved triggers=%d err=%v, want 1", triggerCount, err)
	}
	for table, want := range map[string]string{
		"offline_notifications":        "offline-preserved",
		"traffic_report_notifications": "traffic-preserved",
	} {
		var note string
		if err := db.Table(table).Select("legacy_note").Where("client='node-cascade'").Scan(&note).Error; err != nil {
			t.Fatal(err)
		}
		if note != want {
			t.Fatalf("%s legacy_note=%q, want %q", table, note, want)
		}
		var orphanCount int64
		if err := db.Table(table).Where("client LIKE 'orphan-%'").Count(&orphanCount).Error; err != nil {
			t.Fatal(err)
		}
		if orphanCount != 0 {
			t.Fatalf("%s retained %d historical orphan rows", table, orphanCount)
		}
	}

	if err := db.Exec("DELETE FROM clients WHERE uuid='node-cascade'").Error; err != nil {
		t.Fatalf("delete migrated client: %v", err)
	}
	for _, table := range []string{"offline_notifications", "traffic_report_notifications"} {
		var count int64
		if err := db.Table(table).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("%s rows after cascade=%d err=%v, want 0", table, count, err)
		}
	}
	var violations int64
	if err := db.Raw("SELECT count(*) FROM pragma_foreign_key_check").Scan(&violations).Error; err != nil || violations != 0 {
		t.Fatalf("foreign-key violations=%d err=%v", violations, err)
	}
}

func TestNotificationModelsCreateCascadeForeignKeys(t *testing.T) {
	db, _, _ := openSQLiteForTest(t)
	if err := db.AutoMigrate(
		&models.Client{},
		&models.OfflineNotification{},
		&models.TrafficReportNotification{},
	); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"offline_notifications", "traffic_report_notifications"} {
		var action struct {
			OnDelete string `gorm:"column:on_delete"`
			OnUpdate string `gorm:"column:on_update"`
		}
		if err := db.Raw(
			`SELECT on_delete, on_update
			 FROM pragma_foreign_key_list(?)
			 WHERE "table"='clients' AND "from"='client'`,
			table,
		).Scan(&action).Error; err != nil {
			t.Fatal(err)
		}
		if action.OnDelete != "CASCADE" || action.OnUpdate != "CASCADE" {
			t.Fatalf("%s model foreign-key action = %+v, want CASCADE/CASCADE", table, action)
		}
	}
}

func TestClientNotificationOrphanCleanupRollsBackWithMigrationFailure(t *testing.T) {
	db, primary, _ := openSQLiteForTest(t)
	primary.SetMaxOpenConns(1)
	if err := db.Exec("PRAGMA foreign_keys = OFF").Error; err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"CREATE TABLE clients(uuid TEXT PRIMARY KEY)",
		// Deliberately omit the foreign key so the rebuild fails after its
		// orphan cleanup. The surrounding migration transaction must restore
		// the row rather than leaving a partially repaired schema.
		"CREATE TABLE offline_notifications(client TEXT NOT NULL UNIQUE)",
		"INSERT INTO offline_notifications(client) VALUES('orphan-rollback')",
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
		t.Fatal(err)
	}

	item := schemaMigrations[len(schemaMigrations)-1]
	err := runMigrations(context.Background(), db, []migration{item})
	if err == nil || !strings.Contains(err.Error(), "has 0 client foreign keys") {
		t.Fatalf("migration error=%v, want unsupported-schema failure", err)
	}

	var orphanCount int64
	if err := db.Table("offline_notifications").Where("client = ?", "orphan-rollback").Count(&orphanCount).Error; err != nil {
		t.Fatal(err)
	}
	if orphanCount != 1 {
		t.Fatalf("orphan rows after rollback=%d, want 1", orphanCount)
	}
	version, err := CurrentSchemaVersion(context.Background(), db)
	if err != nil || version != 0 {
		t.Fatalf("schema version after rollback=%d err=%v, want 0", version, err)
	}
}

func queryPlan(t *testing.T, dbQuery func() ([]struct{ Detail string }, error)) string {
	t.Helper()
	rows, err := dbQuery()
	if err != nil {
		t.Fatal(err)
	}
	var details []string
	for _, row := range rows {
		details = append(details, row.Detail)
	}
	return strings.Join(details, " | ")
}

func TestHotPathQueryPlansUseCompositeIndexes(t *testing.T) {
	db, _, _ := openSQLiteForTest(t)
	createLegacyTelemetrySchema(t, db)
	if err := RunMigrations(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		index string
		query string
		args  []any
	}{
		{"record range", "idx_record_client_time", "EXPLAIN QUERY PLAN SELECT * FROM records WHERE client=? AND time>=? AND time<=? ORDER BY time", []any{"node", "2026-01-01", "2026-01-02"}},
		{"gpu range", "idx_gpu_record_client_time_device", "EXPLAIN QUERY PLAN SELECT * FROM gpu_records WHERE client=? AND time>=? AND time<=? ORDER BY time,device_index", []any{"node", "2026-01-01", "2026-01-02"}},
		{"ping range", "idx_ping_record_client_task_time", "EXPLAIN QUERY PLAN SELECT * FROM ping_records WHERE client=? AND task_id=? AND time>=? AND time<=? ORDER BY time", []any{"node", 1, "2026-01-01", "2026-01-02"}},
		{"ping task range", "idx_ping_record_task_time_client", "EXPLAIN QUERY PLAN SELECT * FROM ping_records WHERE task_id=? AND time>=? AND time<=? ORDER BY time", []any{1, "2026-01-01", "2026-01-02"}},
		{"credential digest", "idx_sessions_digest", "EXPLAIN QUERY PLAN SELECT uuid FROM sessions WHERE session_digest=?", []any{make([]byte, 32)}},
		{"session expiry", "idx_sessions_expires", "EXPLAIN QUERY PLAN DELETE FROM sessions WHERE expires<?", []any{"2026-01-01"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var rows []struct{ Detail string }
			plan := queryPlan(t, func() ([]struct{ Detail string }, error) {
				err := db.Raw(tt.query, tt.args...).Scan(&rows).Error
				return rows, err
			})
			if !strings.Contains(plan, tt.index) {
				t.Fatalf("query plan %q does not use %s", plan, tt.index)
			}
		})
	}
}

func TestSchemaMigrationRollbackAndChecksumProtection(t *testing.T) {
	t.Run("rollback", func(t *testing.T) {
		db, _, _ := openSQLiteForTest(t)
		injected := errors.New("injected migration failure")
		failed := []migration{{
			version: 1, name: "rollback_probe",
			statements: []string{"CREATE TABLE rollback_probe(id INTEGER)"},
			up:         func(_ *gorm.DB) error { return injected },
		}}
		if err := runMigrations(context.Background(), db, failed); !errors.Is(err, injected) {
			t.Fatalf("runMigrations() error=%v, want injected failure", err)
		}
		var tableCount, migrationCount int64
		if err := db.Raw("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='rollback_probe'").Scan(&tableCount).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Table(schemaMigrationTable).Count(&migrationCount).Error; err != nil {
			t.Fatal(err)
		}
		version, err := CurrentSchemaVersion(context.Background(), db)
		if err != nil || tableCount != 0 || migrationCount != 0 || version != 0 {
			t.Fatalf("rollback state: table=%d migration=%d version=%d err=%v", tableCount, migrationCount, version, err)
		}
	})

	t.Run("checksum mismatch", func(t *testing.T) {
		db, _, _ := openSQLiteForTest(t)
		original := []migration{{version: 1, name: "checksum_probe", statements: []string{"CREATE TABLE checksum_probe(id INTEGER)"}}}
		if err := runMigrations(context.Background(), db, original); err != nil {
			t.Fatal(err)
		}
		modified := []migration{{version: 1, name: "checksum_probe", statements: []string{"CREATE TABLE checksum_probe(id INTEGER, changed TEXT)"}}}
		if err := runMigrations(context.Background(), db, modified); err == nil || !strings.Contains(err.Error(), "checksum") {
			t.Fatalf("modified migration error=%v, want checksum rejection", err)
		}
	})

	t.Run("newer database", func(t *testing.T) {
		db, _, _ := openSQLiteForTest(t)
		if err := db.Exec("PRAGMA user_version = 99").Error; err != nil {
			t.Fatal(err)
		}
		if err := RunMigrations(context.Background(), db); err == nil || !strings.Contains(err.Error(), "newer") {
			t.Fatalf("newer schema error=%v", err)
		}
	})
}

func BenchmarkHotPathCompositeIndex(b *testing.B) {
	db, _, _ := openSQLiteForTest(b)
	for _, statement := range []string{
		"CREATE TABLE records(client TEXT, time INTEGER, value INTEGER)",
		"CREATE TABLE records_long_term(client TEXT, time INTEGER, value INTEGER)",
		"CREATE TABLE gpu_records(client TEXT, time INTEGER, device_index INTEGER)",
		"CREATE TABLE gpu_records_long_term(client TEXT, time INTEGER, device_index INTEGER)",
		"CREATE TABLE ping_records(client TEXT, task_id INTEGER, time INTEGER, value INTEGER)",
		"CREATE TABLE sessions(session TEXT PRIMARY KEY, session_digest BLOB, expires INTEGER, uuid TEXT)",
		"CREATE INDEX legacy_records_client ON records(client)",
		"CREATE INDEX legacy_records_time ON records(time)",
	} {
		if err := db.Exec(statement).Error; err != nil {
			b.Fatal(err)
		}
	}
	sqlDB, err := db.DB()
	if err != nil {
		b.Fatal(err)
	}
	tx, err := sqlDB.Begin()
	if err != nil {
		b.Fatal(err)
	}
	insert, err := tx.Prepare("INSERT INTO records(client,time,value) VALUES(?,?,?)")
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 100_000; i++ {
		if _, err := insert.Exec(fmt.Sprintf("node-%02d", i%10), i, i); err != nil {
			b.Fatal(err)
		}
	}
	_ = insert.Close()
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	query := func(b *testing.B) {
		b.Helper()
		for i := 0; i < b.N; i++ {
			var count int
			if err := sqlDB.QueryRow("SELECT count(*) FROM records WHERE client=? AND time>=? AND time<=?", "node-05", 90_000, 91_000).Scan(&count); err != nil {
				b.Fatal(err)
			}
			if count != 100 {
				b.Fatalf("count=%d, want 100", count)
			}
		}
	}
	b.Run("legacy-separate-indexes", query)
	if err := RunMigrations(context.Background(), db); err != nil {
		b.Fatal(err)
	}
	b.Run("composite-client-time", query)
}
