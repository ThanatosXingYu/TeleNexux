package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ThanatosXingYu/TeleNexux/internal/backup"
)

const operationTimeout = 15 * time.Minute

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	commandCtx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()

	switch args[0] {
	case "create":
		return runCreate(commandCtx, args[1:], stdout, stderr)
	case "verify":
		return runVerify(commandCtx, args[1:], stdout, stderr)
	case "drill":
		return runDrill(commandCtx, args[1:], stdout, stderr)
	case "restore":
		return runRestore(commandCtx, args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown command %q\n%s", args[0], usage)
	}
}

func runCreate(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
) error {
	flags := newFlagSet("create", stderr)
	source := flags.String("source", "", "application SQLite database path")
	output := flags.String("output", "", "private backup directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*source) == "" || strings.TrimSpace(*output) == "" {
		return errors.New("create requires --source and --output")
	}
	manifest, path, err := backup.Create(ctx, *source, *output, time.Now().UTC())
	if err != nil {
		return err
	}
	return writeJSON(stdout, struct {
		Manifest backup.Manifest `json:"manifest"`
		Backup   string          `json:"backup"`
	}{Manifest: manifest, Backup: path})
}

func runVerify(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
) error {
	flags := newFlagSet("verify", stderr)
	backupPath := flags.String("backup", "", "backup database path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*backupPath) == "" {
		return errors.New("verify requires --backup")
	}
	manifest, err := backup.Verify(ctx, *backupPath)
	if err != nil {
		return err
	}
	return writeJSON(stdout, manifest)
}

func runDrill(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
) error {
	flags := newFlagSet("drill", stderr)
	backupPath := flags.String("backup", "", "backup database path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*backupPath) == "" {
		return errors.New("drill requires --backup")
	}
	report, err := backup.Drill(ctx, *backupPath)
	if err != nil {
		return err
	}
	return writeJSON(stdout, report)
}

func runRestore(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
) error {
	flags := newFlagSet("restore", stderr)
	backupPath := flags.String("backup", "", "backup database path")
	targetPath := flags.String("target", "", "new restore database path; must not exist")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*backupPath) == "" ||
		strings.TrimSpace(*targetPath) == "" {
		return errors.New("restore requires --backup and --target")
	}
	manifest, err := backup.Restore(ctx, *backupPath, *targetPath)
	if err != nil {
		return err
	}
	return writeJSON(stdout, struct {
		Manifest backup.Manifest `json:"manifest"`
		Target   string          `json:"target"`
	}{Manifest: manifest, Target: *targetPath})
}

func newFlagSet(name string, output io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(output)
	return flags
}

func writeJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("write command result: %w", err)
	}
	return nil
}

const usage = `usage:
  telenexus-backup create --source <database> --output <private-directory>
  telenexus-backup verify --backup <backup-database>
  telenexus-backup drill --backup <backup-database>
  telenexus-backup restore --backup <backup-database> --target <new-database>`
