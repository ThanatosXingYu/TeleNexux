package backup_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ThanatosXingYu/TeleNexux/internal/account"
	"github.com/ThanatosXingYu/TeleNexux/internal/backup"
	"github.com/ThanatosXingYu/TeleNexux/internal/operations"
)

func TestCreateVerifyAndDrillBackup(t *testing.T) {
	ctx := context.Background()
	sourcePath := newApplicationDatabase(t)
	outputDirectory := filepath.Join(t.TempDir(), "backups")
	createdAt := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

	manifest, backupPath, err := backup.Create(
		ctx,
		sourcePath,
		outputDirectory,
		createdAt,
	)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if manifest.Database != filepath.Base(backupPath) ||
		manifest.SizeBytes <= 0 ||
		len(manifest.SHA256) != 64 {
		t.Fatalf("Create() manifest = %+v", manifest)
	}
	if info, err := os.Stat(backupPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("backup file info = %+v, %v", info, err)
	}
	if info, err := os.Stat(outputDirectory); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("backup directory info = %+v, %v", info, err)
	}

	verified, err := backup.Verify(ctx, backupPath)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !reflect.DeepEqual(verified, manifest) {
		t.Fatalf("Verify() = %+v, want %+v", verified, manifest)
	}
	report, err := backup.Drill(ctx, backupPath)
	if err != nil {
		t.Fatalf("Drill() error = %v", err)
	}
	if report.Integrity != "ok" ||
		report.RestoredBytes != manifest.SizeBytes ||
		report.TableCounts["accounts"] != 1 ||
		report.TableCounts["audit_events"] != 1 ||
		report.TableCounts["ingestion_batches"] != 1 ||
		report.TableCounts["evidence_events"] != 1 ||
		report.TableCounts["legal_holds"] != 1 {
		t.Fatalf("Drill() = %+v", report)
	}
}

func TestCreateRejectsInvalidSourcesAndInsecureOutput(t *testing.T) {
	ctx := context.Background()
	t.Run("missing required table", func(t *testing.T) {
		sourcePath := filepath.Join(t.TempDir(), "incomplete.db")
		db, err := sql.Open("sqlite", sourcePath)
		if err != nil {
			t.Fatalf("sql.Open() error = %v", err)
		}
		if _, err := db.ExecContext(ctx, "CREATE TABLE accounts (id INTEGER PRIMARY KEY)"); err != nil {
			_ = db.Close()
			t.Fatalf("create incomplete schema: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close incomplete database: %v", err)
		}

		outputDirectory := filepath.Join(t.TempDir(), "backups")
		_, _, err = backup.Create(ctx, sourcePath, outputDirectory, time.Now())
		if err == nil || !strings.Contains(err.Error(), "missing required table") {
			t.Fatalf("Create() error = %v, want missing required table", err)
		}
		assertDirectoryEmpty(t, outputDirectory)
	})

	t.Run("symlink source", func(t *testing.T) {
		sourcePath := newApplicationDatabase(t)
		linkPath := filepath.Join(t.TempDir(), "source.db")
		if err := os.Symlink(sourcePath, linkPath); err != nil {
			t.Fatalf("Symlink() error = %v", err)
		}
		_, _, err := backup.Create(ctx, linkPath, filepath.Join(t.TempDir(), "backups"), time.Now())
		if err == nil || !strings.Contains(err.Error(), "not a symlink") {
			t.Fatalf("Create() error = %v, want symlink rejection", err)
		}
	})

	t.Run("insecure output directory", func(t *testing.T) {
		sourcePath := newApplicationDatabase(t)
		outputDirectory := filepath.Join(t.TempDir(), "backups")
		if err := os.Mkdir(outputDirectory, 0o755); err != nil {
			t.Fatalf("Mkdir() error = %v", err)
		}
		if err := os.Chmod(outputDirectory, 0o755); err != nil {
			t.Fatalf("Chmod() error = %v", err)
		}
		_, _, err := backup.Create(ctx, sourcePath, outputDirectory, time.Now())
		if err == nil || !strings.Contains(err.Error(), "must not grant group or other access") {
			t.Fatalf("Create() error = %v, want insecure directory rejection", err)
		}
	})
}

func TestVerifyRejectsTamperedDataAndManifest(t *testing.T) {
	ctx := context.Background()
	manifest, backupPath, err := backup.Create(
		ctx,
		newApplicationDatabase(t),
		filepath.Join(t.TempDir(), "backups"),
		time.Now(),
	)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	tampered := manifest
	tampered.SHA256 = strings.Repeat("0", 64)
	writeManifest(t, backup.ManifestPath(backupPath), tampered)
	if _, err := backup.Verify(ctx, backupPath); err == nil ||
		!strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("Verify() error = %v, want hash rejection", err)
	}

	writeManifest(t, backup.ManifestPath(backupPath), manifest)
	file, err := os.OpenFile(backupPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile() error = %v", err)
	}
	if _, err := file.Write([]byte("tampered")); err != nil {
		_ = file.Close()
		t.Fatalf("append tampered data: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close tampered backup: %v", err)
	}
	if _, err := backup.Verify(ctx, backupPath); err == nil ||
		!strings.Contains(err.Error(), "size") {
		t.Fatalf("Verify() error = %v, want size rejection", err)
	}
}

func TestRestoreNeverOverwritesOrLeavesPartialFiles(t *testing.T) {
	ctx := context.Background()
	manifest, backupPath, err := backup.Create(
		ctx,
		newApplicationDatabase(t),
		filepath.Join(t.TempDir(), "backups"),
		time.Now(),
	)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	targetDirectory := filepath.Join(t.TempDir(), "restore")
	if err := os.Mkdir(targetDirectory, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	targetPath := filepath.Join(targetDirectory, "telenexus.db")
	if err := os.WriteFile(targetPath, []byte("sentinel"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := backup.Restore(ctx, backupPath, targetPath); err == nil ||
		!strings.Contains(err.Error(), "already exists") {
		t.Fatalf("Restore() error = %v, want existing target rejection", err)
	}
	content, err := os.ReadFile(targetPath)
	if err != nil || string(content) != "sentinel" {
		t.Fatalf("restore target content = %q, %v", content, err)
	}

	if err := os.Remove(targetPath); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	file, err := os.OpenFile(backupPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("OpenFile() error = %v", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		t.Fatalf("Stat() error = %v", err)
	}
	last := make([]byte, 1)
	if _, err := file.ReadAt(last, info.Size()-1); err != nil {
		_ = file.Close()
		t.Fatalf("ReadAt() error = %v", err)
	}
	last[0] ^= 0xff
	if _, err := file.WriteAt(last, info.Size()-1); err != nil {
		_ = file.Close()
		t.Fatalf("WriteAt() error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close corrupt backup: %v", err)
	}

	if _, err := backup.Restore(ctx, backupPath, targetPath); err == nil ||
		!strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("Restore() error = %v, want corruption rejection", err)
	}
	if _, err := os.Lstat(targetPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restore target exists after failure: %v", err)
	}
	temporaryFiles, err := filepath.Glob(filepath.Join(targetDirectory, ".telenexus-restore-*"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(temporaryFiles) != 0 {
		t.Fatalf("temporary restore files = %v", temporaryFiles)
	}

	if err := os.Remove(backupPath); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	writeManifest(t, backup.ManifestPath(backupPath), manifest)
}

func TestRestoreRejectsSymlinkAndInsecureParent(t *testing.T) {
	ctx := context.Background()
	_, backupPath, err := backup.Create(
		ctx,
		newApplicationDatabase(t),
		filepath.Join(t.TempDir(), "backups"),
		time.Now(),
	)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	t.Run("symlink parent", func(t *testing.T) {
		realDirectory := filepath.Join(t.TempDir(), "real")
		if err := os.Mkdir(realDirectory, 0o700); err != nil {
			t.Fatalf("Mkdir() error = %v", err)
		}
		linkDirectory := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(realDirectory, linkDirectory); err != nil {
			t.Fatalf("Symlink() error = %v", err)
		}
		_, err := backup.Restore(
			ctx,
			backupPath,
			filepath.Join(linkDirectory, "restored.db"),
		)
		if err == nil || !strings.Contains(err.Error(), "not contain symlinks") {
			t.Fatalf("Restore() error = %v, want symlink rejection", err)
		}
	})

	t.Run("insecure parent", func(t *testing.T) {
		directory := filepath.Join(t.TempDir(), "restore")
		if err := os.Mkdir(directory, 0o755); err != nil {
			t.Fatalf("Mkdir() error = %v", err)
		}
		if err := os.Chmod(directory, 0o755); err != nil {
			t.Fatalf("Chmod() error = %v", err)
		}
		_, err := backup.Restore(ctx, backupPath, filepath.Join(directory, "restored.db"))
		if err == nil || !strings.Contains(err.Error(), "must not grant group or other access") {
			t.Fatalf("Restore() error = %v, want insecure directory rejection", err)
		}
	})
}

func TestBackupRejectsSymlinkInAncestorDirectory(t *testing.T) {
	ctx := context.Background()
	sourcePath := newApplicationDatabase(t)
	root := t.TempDir()
	realDirectory := filepath.Join(root, "real")
	if err := os.Mkdir(realDirectory, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	linkDirectory := filepath.Join(root, "link")
	if err := os.Symlink(realDirectory, linkDirectory); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	outputDirectory := filepath.Join(linkDirectory, "nested", "backups")
	_, _, err := backup.Create(ctx, sourcePath, outputDirectory, time.Now())
	if err == nil || !strings.Contains(err.Error(), "not contain symlinks") {
		t.Fatalf("Create() error = %v, want ancestor symlink rejection", err)
	}

	existingOutput := filepath.Join(realDirectory, "backups")
	_, backupPath, err := backup.Create(ctx, sourcePath, existingOutput, time.Now())
	if err != nil {
		t.Fatalf("Create() setup error = %v", err)
	}
	_, err = backup.Restore(
		ctx,
		backupPath,
		filepath.Join(linkDirectory, "nested", "restored.db"),
	)
	if err == nil || !strings.Contains(err.Error(), "not contain symlinks") {
		t.Fatalf("Restore() error = %v, want ancestor symlink rejection", err)
	}
}

func newApplicationDatabase(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "telenexus.db")
	repository, err := account.NewSQLiteRepository(ctx, path)
	if err != nil {
		t.Fatalf("NewSQLiteRepository() error = %v", err)
	}
	t.Cleanup(func() {
		if err := repository.Close(); err != nil {
			t.Errorf("close account repository: %v", err)
		}
	})
	accounts := account.NewService(repository)
	if _, err := accounts.Create(ctx, account.CreateInput{
		ID:          42,
		PhoneNumber: "+15550001042",
		Username:    "backup_test",
		DisplayName: "Backup Test",
	}); err != nil {
		t.Fatalf("Create() account error = %v", err)
	}

	store, err := operations.Open(ctx, path)
	if err != nil {
		t.Fatalf("operations.Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close operations store: %v", err)
		}
	})
	if err := store.RecordAudit(ctx, operations.AuditEventInput{
		RequestID:  "backup-test-request",
		Actor:      "backup_test",
		Role:       "administrator",
		Method:     "POST",
		Route:      "/api/v1/test",
		StatusCode: 200,
	}); err != nil {
		t.Fatalf("RecordAudit() error = %v", err)
	}
	if _, err := store.CreateLegalHold(ctx, operations.LegalHoldInput{
		Scope:     "account",
		AccountID: 42,
		Reason:    "backup test hold",
		CreatedBy: "backup_test",
	}); err != nil {
		t.Fatalf("CreateLegalHold() error = %v", err)
	}
	if _, err := store.IngestBatch(ctx, operations.IngestBatchInput{
		BatchID:          "backup-test-batch",
		Source:           "test",
		AccountID:        42,
		PeerType:         "chat",
		PeerID:           100,
		PeerTitle:        "Backup Test",
		CheckpointBefore: 0,
		CheckpointAfter:  1,
		Complete:         true,
		Messages: []operations.MessageRecord{{
			AccountID: 42,
			PeerType:  "chat",
			PeerID:    100,
			PeerTitle: "Backup Test",
			MessageID: 1,
			Sender:    "tester",
			Text:      "backup fixture",
			SentAt:    time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC),
		}},
	}); err != nil {
		t.Fatalf("IngestBatch() error = %v", err)
	}
	return path
}

func writeManifest(t *testing.T, path string, manifest backup.Manifest) {
	t.Helper()
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("WriteFile() manifest error = %v", err)
	}
}

func assertDirectoryEmpty(t *testing.T, path string) {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("directory entries = %v", entries)
	}
}
