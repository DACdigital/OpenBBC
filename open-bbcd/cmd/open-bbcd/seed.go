package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/database"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/repository"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/seed"
)

const seedUsage = "usage: open-bbcd seed validate [--no-env] <path> | seed apply <path>"

// usageError marks a command-line misuse; main exits 2 for it.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

// loadSeed runs Load → Validate and, unless skipEnv, Interpolate against the
// process environment. Apply does not validate, so every caller goes here.
func loadSeed(path string, skipEnv bool) (*seed.Set, error) {
	set, err := seed.Load(path)
	if err != nil {
		return nil, err
	}
	if err := seed.Validate(set); err != nil {
		return nil, err
	}
	if !skipEnv {
		if err := seed.Interpolate(set, os.LookupEnv); err != nil {
			return nil, err
		}
	}
	return set, nil
}

// runBootSeed is the boot-time seed step. An empty path means seeding is off:
// previously seeded backends are unlocked. Any error must abort boot.
func runBootSeed(ctx context.Context, db *sql.DB, path string, logger *slog.Logger) error {
	repo := repository.NewSeedRepository(db)
	if path == "" {
		return repo.ClearManagedFlags(ctx)
	}
	set, err := loadSeed(path, false)
	if err != nil {
		return err
	}
	actions, err := repo.Apply(ctx, set, repository.ApplyOptions{Boot: true})
	if err != nil {
		return err
	}
	for _, a := range actions {
		logger.Info("seed", "kind", a.Kind, "key", a.Key, "action", a.Action, "detail", a.Detail)
	}
	return nil
}

// runSeedCmd implements `open-bbcd seed validate|apply`.
func runSeedCmd(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return &usageError{seedUsage}
	}
	switch args[0] {
	case "validate":
		fs := flag.NewFlagSet("seed validate", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		noEnv := fs.Bool("no-env", false, "skip env interpolation; list required variables")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 1 {
			return &usageError{seedUsage}
		}
		set, err := loadSeed(fs.Arg(0), *noEnv)
		if err != nil {
			return err
		}
		if *noEnv {
			for _, n := range seed.RequiredEnv(set) {
				fmt.Fprintln(stdout, n)
			}
		}
		return nil
	case "apply":
		if len(args) != 2 {
			return &usageError{seedUsage}
		}
		dsn := os.Getenv("DATABASE_URL")
		if dsn == "" {
			return errors.New("DATABASE_URL is not set")
		}
		set, err := loadSeed(args[1], false)
		if err != nil {
			return err
		}
		db, err := database.NewPostgres(dsn)
		if err != nil {
			return fmt.Errorf("connect to database: %w", err)
		}
		defer db.Close()
		actions, err := repository.NewSeedRepository(db).Apply(context.Background(), set, repository.ApplyOptions{})
		if err != nil {
			return err
		}
		for _, a := range actions {
			fmt.Fprintf(stdout, "%s %s %s %s\n", a.Kind, a.Key, a.Action, a.Detail)
		}
		return nil
	default:
		return &usageError{seedUsage}
	}
}
