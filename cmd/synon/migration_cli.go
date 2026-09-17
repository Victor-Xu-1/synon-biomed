package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"synon-go/internal/datadir"
	v11migration "synon-go/internal/migration/v11"
)

func runV11MigrationCLI(ctx context.Context, args []string, output io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: synon-go migration-v1.1 <inspect|migrate|verify|activate|rollback-cutover|rollback> [options]")
	}
	switch args[0] {
	case "inspect":
		flags := newMigrationFlagSet("migration-v1.1 inspect")
		sourceDB := flags.String("source-db", "", "absolute path to the v1.1 operon-cli.db")
		if err := parseMigrationFlags(flags, args[1:]); err != nil {
			return err
		}
		if strings.TrimSpace(*sourceDB) == "" {
			return errors.New("--source-db is required")
		}
		inspection, err := v11migration.Inspect(ctx, *sourceDB)
		if err != nil {
			return err
		}
		return writeMigrationJSON(output, inspection)
	case "migrate":
		flags := newMigrationFlagSet("migration-v1.1 migrate")
		sourceDB := flags.String("source-db", "", "absolute path to the v1.1 operon-cli.db")
		sourceDataDir := flags.String("source-data-dir", "", "v1.1 data directory containing artifacts and preferences")
		targetHome := flags.String("target-home", "", "new Go runtime data directory")
		conflict := flags.String("conflict", string(v11migration.ConflictAbort), "target policy: abort or replace")
		if err := parseMigrationFlags(flags, args[1:]); err != nil {
			return err
		}
		if strings.TrimSpace(*sourceDB) == "" || strings.TrimSpace(*targetHome) == "" {
			return errors.New("--source-db and --target-home are required")
		}
		report, err := v11migration.Migrate(ctx, v11migration.Options{
			SourceDB: *sourceDB, SourceDataDir: *sourceDataDir, TargetHome: *targetHome,
			ConflictPolicy: v11migration.ConflictPolicy(strings.TrimSpace(*conflict)),
		})
		if err != nil {
			return err
		}
		return writeMigrationJSON(output, report)
	case "verify":
		flags := newMigrationFlagSet("migration-v1.1 verify")
		targetHome := flags.String("target-home", "", "migrated Go runtime data directory")
		if err := parseMigrationFlags(flags, args[1:]); err != nil {
			return err
		}
		if strings.TrimSpace(*targetHome) == "" {
			return errors.New("--target-home is required")
		}
		verification, err := v11migration.Verify(ctx, *targetHome)
		if err != nil {
			return err
		}
		return writeMigrationJSON(output, verification)
	case "activate":
		flags := newMigrationFlagSet("migration-v1.1 activate")
		targetHome := flags.String("target-home", "", "verified migrated Go runtime data directory")
		controlPath := flags.String("data-dir-control", "", "absolute Go data-directory control JSON path")
		if err := parseMigrationFlags(flags, args[1:]); err != nil {
			return err
		}
		if strings.TrimSpace(*targetHome) == "" || strings.TrimSpace(*controlPath) == "" {
			return errors.New("--target-home and --data-dir-control are required")
		}
		verification, err := v11migration.Verify(ctx, *targetHome)
		if err != nil {
			return err
		}
		activation, err := datadir.New(*controlPath).ActivateExisting(verification.TargetHome, verification.MigrationID)
		if err != nil {
			return err
		}
		return writeMigrationJSON(output, map[string]any{"verification": verification, "activation": activation})
	case "rollback-cutover":
		flags := newMigrationFlagSet("migration-v1.1 rollback-cutover")
		controlPath := flags.String("data-dir-control", "", "absolute Go data-directory control JSON path")
		activationID := flags.String("activation-id", "", "activation identifier returned by activate")
		if err := parseMigrationFlags(flags, args[1:]); err != nil {
			return err
		}
		if strings.TrimSpace(*controlPath) == "" || strings.TrimSpace(*activationID) == "" {
			return errors.New("--data-dir-control and --activation-id are required")
		}
		activation, err := datadir.New(*controlPath).RollbackActivation(*activationID)
		if err != nil {
			return err
		}
		return writeMigrationJSON(output, map[string]any{"status": "rolled_back", "activation": activation})
	case "rollback":
		flags := newMigrationFlagSet("migration-v1.1 rollback")
		targetHome := flags.String("target-home", "", "active migrated Go runtime data directory")
		migrationID := flags.String("migration-id", "", "migration identifier from MIGRATION_V1_1.json")
		if err := parseMigrationFlags(flags, args[1:]); err != nil {
			return err
		}
		if strings.TrimSpace(*targetHome) == "" || strings.TrimSpace(*migrationID) == "" {
			return errors.New("--target-home and --migration-id are required")
		}
		report, err := v11migration.Rollback(ctx, *targetHome, *migrationID)
		if err != nil {
			return err
		}
		return writeMigrationJSON(output, report)
	default:
		return fmt.Errorf("unknown migration-v1.1 action %q", args[0])
	}
}

func newMigrationFlagSet(name string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	return flags
}

func parseMigrationFlags(flags *flag.FlagSet, args []string) error {
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	return nil
}

func writeMigrationJSON(output io.Writer, value any) error {
	if output == nil {
		return errors.New("migration output writer is required")
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
