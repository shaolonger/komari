package cmd

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/komari-monitor/komari/cmd/flags"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/spf13/cobra"
)

var (
	databaseBackupOutput  string
	databaseBackupTimeout time.Duration
)

var databaseBackupCmd = &cobra.Command{
	Use:   "database-backup",
	Short: "Create an atomic SQLite database backup",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if flags.DatabaseType != "" && flags.DatabaseType != "sqlite" {
			return errors.New("database-backup currently supports SQLite only")
		}
		if databaseBackupOutput == "" {
			return errors.New("--output is required")
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), databaseBackupTimeout)
		defer cancel()
		if err := dbcore.BackupSQLite(ctx, flags.DatabaseFile, databaseBackupOutput); err != nil {
			return fmt.Errorf("database backup failed: %w", err)
		}
		cmd.Printf("SQLite backup created: %s\n", databaseBackupOutput)
		return nil
	},
}

func init() {
	databaseBackupCmd.Flags().StringVarP(&databaseBackupOutput, "output", "o", "", "destination backup file (must not already exist)")
	databaseBackupCmd.Flags().DurationVar(&databaseBackupTimeout, "timeout", 10*time.Minute, "maximum backup duration")
	RootCmd.AddCommand(databaseBackupCmd)
}
