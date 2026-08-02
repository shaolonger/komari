package dbcore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/komari-monitor/komari/internal/runtimeprofile"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openSQLiteForTest(t testing.TB) (*gorm.DB, *sql.DB, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "database with spaces.db")
	db, writer, err := openSQLiteDatabase(path, logger.Default.LogMode(logger.Silent))
	if err != nil {
		t.Fatal(err)
	}
	primary, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = writer.Close()
		_ = primary.Close()
	})
	return db, primary, writer
}

type sqlitePragmas struct {
	journalMode string
	foreignKeys int
	busyTimeout int
	synchronous int
	cacheSize   int
	tempStore   int
	mmapSize    int64
}

func readSQLitePragmas(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (sqlitePragmas, error) {
	var result sqlitePragmas
	checks := []struct {
		query string
		dest  any
	}{
		{"PRAGMA journal_mode", &result.journalMode},
		{"PRAGMA foreign_keys", &result.foreignKeys},
		{"PRAGMA busy_timeout", &result.busyTimeout},
		{"PRAGMA synchronous", &result.synchronous},
		{"PRAGMA cache_size", &result.cacheSize},
		{"PRAGMA temp_store", &result.tempStore},
		{"PRAGMA mmap_size", &result.mmapSize},
	}
	for _, check := range checks {
		if err := queryer.QueryRowContext(ctx, check.query).Scan(check.dest); err != nil {
			return sqlitePragmas{}, fmt.Errorf("%s: %w", check.query, err)
		}
	}
	return result, nil
}

func assertSQLitePragmas(t testing.TB, settings sqlitePragmas) {
	t.Helper()
	profile, err := runtimeprofile.Current()
	if err != nil {
		t.Fatal(err)
	}
	if strings.ToLower(settings.journalMode) != "wal" {
		t.Fatalf("journal_mode = %q, want wal", settings.journalMode)
	}
	if settings.foreignKeys != 1 || settings.busyTimeout != SQLiteBusyTimeoutMillis {
		t.Fatalf("integrity settings = %+v", settings)
	}
	wantTempStore := 2
	if profile.SQLiteTempStore == "FILE" {
		wantTempStore = 1
	}
	if settings.synchronous != 1 || settings.cacheSize != -profile.SQLiteCacheKiB || settings.tempStore != wantTempStore {
		t.Fatalf("performance settings = %+v", settings)
	}
	if settings.mmapSize < profile.SQLiteMmapBytes {
		t.Fatalf("mmap_size = %d, want >= %d", settings.mmapSize, profile.SQLiteMmapBytes)
	}
}

func TestSQLitePoolAppliesPragmasToEveryConnection(t *testing.T) {
	_, primary, writer := openSQLiteForTest(t)
	if got, want := primary.Stats().MaxOpenConnections, readConnectionLimit(); got != want {
		t.Fatalf("primary MaxOpenConnections = %d, want %d", got, want)
	}
	if got := writer.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("writer MaxOpenConnections = %d, want 1", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connections := make([]*sql.Conn, 0, min(4, readConnectionLimit()))
	for i := 0; i < cap(connections); i++ {
		connection, err := primary.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, connection)
	}
	defer func() {
		for _, connection := range connections {
			_ = connection.Close()
		}
	}()
	for index, connection := range connections {
		settings, err := readSQLitePragmas(ctx, connection)
		if err != nil {
			t.Fatalf("connection %d: %v", index, err)
		}
		assertSQLitePragmas(t, settings)
	}
	settings, err := readSQLitePragmas(ctx, writer)
	if err != nil {
		t.Fatal(err)
	}
	assertSQLitePragmas(t, settings)
}

func TestSQLiteWALReaderAndDedicatedWriter(t *testing.T) {
	db, primary, writer := openSQLiteForTest(t)
	if err := db.Exec("CREATE TABLE wal_probe(id INTEGER PRIMARY KEY, value TEXT)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO wal_probe(value) VALUES(?)", "before").Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	reader, err := primary.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Rollback()
	var count int
	if err := reader.QueryRowContext(ctx, "SELECT count(*) FROM wal_probe").Scan(&count); err != nil || count != 1 {
		t.Fatalf("initial reader snapshot count=%d err=%v", count, err)
	}

	writeTx, err := writer.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writeTx.ExecContext(ctx, "INSERT INTO wal_probe(value) VALUES(?)", "during-reader"); err != nil {
		_ = writeTx.Rollback()
		t.Fatal(err)
	}
	if err := writeTx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := reader.QueryRowContext(ctx, "SELECT count(*) FROM wal_probe").Scan(&count); err != nil || count != 1 {
		t.Fatalf("reader snapshot changed during WAL write: count=%d err=%v", count, err)
	}
	if err := reader.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := primary.QueryRowContext(ctx, "SELECT count(*) FROM wal_probe").Scan(&count); err != nil || count != 2 {
		t.Fatalf("post-commit count=%d err=%v", count, err)
	}
}

func TestSQLiteForeignKeysAreEnforcedOnWriterPool(t *testing.T) {
	db, _, writer := openSQLiteForTest(t)
	if err := db.Exec("CREATE TABLE parents(id INTEGER PRIMARY KEY)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TABLE children(id INTEGER PRIMARY KEY, parent_id INTEGER REFERENCES parents(id))").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec("INSERT INTO children(parent_id) VALUES(999)"); err == nil {
		t.Fatal("dedicated writer accepted a foreign-key violation")
	}
}

func TestBackupSQLiteIncludesCommittedWALData(t *testing.T) {
	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "live.db")
	destinationPath := filepath.Join(directory, "backups", "snapshot.db")
	database, writer, err := openSQLiteDatabase(sourcePath, logger.Default.LogMode(logger.Silent))
	if err != nil {
		t.Fatal(err)
	}
	primary, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer primary.Close()
	defer writer.Close()
	if err := database.Exec("CREATE TABLE backup_probe(id INTEGER PRIMARY KEY, value TEXT)").Error; err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 100; index++ {
		if err := database.Exec("INSERT INTO backup_probe(value) VALUES(?)", fmt.Sprintf("value-%d", index)).Error; err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := BackupSQLite(ctx, sourcePath, destinationPath); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(destinationPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("backup permissions = %o, want 600", info.Mode().Perm())
	}
	backup, err := sql.Open(komariSQLiteBackupDriver, destinationPath)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	var count int
	if err := backup.QueryRowContext(ctx, "SELECT count(*) FROM backup_probe").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 100 {
		t.Fatalf("backup row count = %d, want 100", count)
	}
	if err := BackupSQLite(ctx, sourcePath, destinationPath); err == nil {
		t.Fatal("expected existing destination to be rejected")
	}
}

func TestBackupSQLiteHonorsCanceledContext(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "source.db")
	if err := os.WriteFile(sourcePath, []byte("not opened"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := BackupSQLite(ctx, sourcePath, filepath.Join(t.TempDir(), "backup.db")); !errors.Is(err, context.Canceled) {
		t.Fatalf("BackupSQLite error = %v, want context.Canceled", err)
	}
}
