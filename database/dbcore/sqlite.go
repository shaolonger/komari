package dbcore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"

	"github.com/mattn/go-sqlite3"
	gormsqlite "gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	komariSQLiteDriver      = "komari_sqlite3"
	SQLiteBusyTimeoutMillis = 5_000
	SQLiteCacheKiB          = 8_192
	SQLiteMmapBytes         = 256 * 1024 * 1024
	MaxReadConnections      = 8
)

var memoryDatabaseSequence atomic.Uint64
var writerInstance *sql.DB

func init() {
	sql.Register(komariSQLiteDriver, &sqlite3.SQLiteDriver{ConnectHook: configureSQLiteConnection})
}

func configureSQLiteConnection(connection *sqlite3.SQLiteConn) error {
	pragmas := []string{
		"PRAGMA foreign_keys = ON",
		fmt.Sprintf("PRAGMA busy_timeout = %d", SQLiteBusyTimeoutMillis),
		"PRAGMA synchronous = NORMAL",
		fmt.Sprintf("PRAGMA cache_size = -%d", SQLiteCacheKiB),
		"PRAGMA temp_store = MEMORY",
		fmt.Sprintf("PRAGMA mmap_size = %d", SQLiteMmapBytes),
	}
	for _, pragma := range pragmas {
		if _, err := connection.Exec(pragma, []driver.Value{}); err != nil {
			return fmt.Errorf("apply %q: %w", pragma, err)
		}
	}
	return nil
}

func sqliteBaseURI(path string) (string, error) {
	// Some library-style and package-test callers initialize Komari without the
	// command layer that supplies a database path. Preserve the historical
	// ephemeral-database behavior while still giving every pool a named shared
	// database; the server command always provides an explicit path.
	if path == "" || path == ":memory:" {
		return fmt.Sprintf("file:komari-memory-%d?mode=memory&cache=shared", memoryDatabaseSequence.Add(1)), nil
	}
	if strings.HasPrefix(path, "file:") {
		return path, nil
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(absolute)}).String(), nil
}

func addSQLiteParameters(base string, writer bool) string {
	separator := "?"
	if strings.Contains(base, "?") {
		separator = "&"
	}
	parameters := fmt.Sprintf(
		"_busy_timeout=%d&_foreign_keys=on&_journal_mode=WAL&_synchronous=NORMAL&_cache_size=-%d",
		SQLiteBusyTimeoutMillis, SQLiteCacheKiB,
	)
	if writer {
		parameters += "&_txlock=immediate"
	}
	return base + separator + parameters
}

func readConnectionLimit() int {
	return min(max(runtime.GOMAXPROCS(0), 4), MaxReadConnections)
}

func configureReadPool(database *sql.DB) {
	limit := readConnectionLimit()
	database.SetMaxOpenConns(limit)
	database.SetMaxIdleConns(limit)
	database.SetConnMaxLifetime(0)
	database.SetConnMaxIdleTime(0)
}

func configureWriterPool(database *sql.DB) {
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	database.SetConnMaxLifetime(0)
	database.SetConnMaxIdleTime(0)
}

func openSQLiteDatabase(path string, gormLogger logger.Interface) (*gorm.DB, *sql.DB, error) {
	base, err := sqliteBaseURI(path)
	if err != nil {
		return nil, nil, err
	}
	primary, err := gorm.Open(gormsqlite.New(gormsqlite.Config{
		DriverName: komariSQLiteDriver,
		DSN:        addSQLiteParameters(base, false),
	}), &gorm.Config{Logger: gormLogger})
	if err != nil {
		return nil, nil, err
	}
	primarySQL, err := primary.DB()
	if err != nil {
		return nil, nil, err
	}
	configureReadPool(primarySQL)

	writer, err := sql.Open(komariSQLiteDriver, addSQLiteParameters(base, true))
	if err != nil {
		_ = primarySQL.Close()
		return nil, nil, err
	}
	configureWriterPool(writer)
	if err := writer.PingContext(context.Background()); err != nil {
		_ = writer.Close()
		_ = primarySQL.Close()
		return nil, nil, err
	}
	return primary, writer, nil
}

// GetWriterDBInstance returns the dedicated single-connection write pool used
// by bounded background writers. General GORM reads use the WAL read pool.
func GetWriterDBInstance() (*sql.DB, error) {
	GetDBInstance()
	if writerInstance == nil {
		return nil, errors.New("SQLite writer pool is not initialized")
	}
	return writerInstance, nil
}
