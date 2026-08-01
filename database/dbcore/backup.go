package dbcore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mattn/go-sqlite3"
)

const komariSQLiteBackupDriver = "komari_sqlite3_backup"

func init() {
	sql.Register(komariSQLiteBackupDriver, &sqlite3.SQLiteDriver{})
}

// BackupSQLite creates an atomic, transactionally consistent SQLite snapshot.
// It uses SQLite's online backup API and therefore includes committed WAL data
// without requiring the sqlite3 command-line program or stopping the server.
func BackupSQLite(ctx context.Context, sourcePath, destinationPath string) (resultErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(sourcePath) == "" || strings.TrimSpace(destinationPath) == "" {
		return errors.New("source and destination paths are required")
	}

	sourceAbsolute, err := filepath.Abs(sourcePath)
	if err != nil {
		return fmt.Errorf("resolve source path: %w", err)
	}
	destinationAbsolute, err := filepath.Abs(destinationPath)
	if err != nil {
		return fmt.Errorf("resolve destination path: %w", err)
	}
	if filepath.Clean(sourceAbsolute) == filepath.Clean(destinationAbsolute) {
		return errors.New("backup destination must differ from source")
	}
	info, err := os.Stat(sourceAbsolute)
	if err != nil {
		return fmt.Errorf("stat source database: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("source database is not a regular file")
	}
	if _, err := os.Lstat(destinationAbsolute); err == nil {
		return errors.New("backup destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat backup destination: %w", err)
	}

	destinationDir := filepath.Dir(destinationAbsolute)
	if err := os.MkdirAll(destinationDir, 0o700); err != nil {
		return fmt.Errorf("create backup directory: %w", err)
	}
	temporary, err := os.CreateTemp(destinationDir, ".komari-backup-*.partial")
	if err != nil {
		return fmt.Errorf("create temporary backup: %w", err)
	}
	temporaryPath := temporary.Name()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("secure temporary backup: %w", err)
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("close temporary backup: %w", err)
	}
	defer func() {
		if resultErr != nil {
			_ = os.Remove(temporaryPath)
		}
	}()

	sourceBase, err := sqliteBaseURI(sourceAbsolute)
	if err != nil {
		return err
	}
	temporaryBase, err := sqliteBaseURI(temporaryPath)
	if err != nil {
		return err
	}
	sourceDatabase, err := sql.Open(komariSQLiteBackupDriver, sourceBase+"?mode=ro&_busy_timeout=5000")
	if err != nil {
		return fmt.Errorf("open source database: %w", err)
	}
	defer sourceDatabase.Close()
	destinationDatabase, err := sql.Open(komariSQLiteBackupDriver, temporaryBase+"?mode=rw&_busy_timeout=5000")
	if err != nil {
		return fmt.Errorf("open backup database: %w", err)
	}
	defer destinationDatabase.Close()
	sourceDatabase.SetMaxOpenConns(1)
	destinationDatabase.SetMaxOpenConns(1)

	sourceConnection, err := sourceDatabase.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire source connection: %w", err)
	}
	defer sourceConnection.Close()
	destinationConnection, err := destinationDatabase.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire backup connection: %w", err)
	}
	defer destinationConnection.Close()

	err = sourceConnection.Raw(func(sourceDriver any) error {
		sourceSQLite, ok := sourceDriver.(*sqlite3.SQLiteConn)
		if !ok {
			return errors.New("unexpected source SQLite driver")
		}
		return destinationConnection.Raw(func(destinationDriver any) error {
			destinationSQLite, ok := destinationDriver.(*sqlite3.SQLiteConn)
			if !ok {
				return errors.New("unexpected destination SQLite driver")
			}
			backup, err := destinationSQLite.Backup("main", sourceSQLite, "main")
			if err != nil {
				return err
			}
			for {
				if err := ctx.Err(); err != nil {
					_ = backup.Finish()
					return err
				}
				done, stepErr := backup.Step(256)
				if stepErr != nil {
					_ = backup.Finish()
					return stepErr
				}
				if done {
					return backup.Finish()
				}
				timer := time.NewTimer(5 * time.Millisecond)
				select {
				case <-ctx.Done():
					timer.Stop()
					_ = backup.Finish()
					return ctx.Err()
				case <-timer.C:
				}
			}
		})
	})
	if err != nil {
		return fmt.Errorf("copy SQLite database: %w", err)
	}

	var integrity string
	if err := destinationConnection.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		return fmt.Errorf("verify SQLite backup: %w", err)
	}
	if integrity != "ok" {
		return fmt.Errorf("verify SQLite backup: %s", integrity)
	}
	if err := destinationConnection.Close(); err != nil {
		return fmt.Errorf("close backup connection: %w", err)
	}
	if err := destinationDatabase.Close(); err != nil {
		return fmt.Errorf("close backup database: %w", err)
	}
	if err := os.Rename(temporaryPath, destinationAbsolute); err != nil {
		return fmt.Errorf("publish SQLite backup: %w", err)
	}
	return nil
}
