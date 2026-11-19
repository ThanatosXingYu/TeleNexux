// Package backup creates and verifies privileged local SQLite snapshots.
package backup

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	FormatVersion = 1
	maxManifest   = 1 << 20
)

var requiredTables = []string{
	"accounts",
	"account_destructions",
	"audit_events",
	"evidence_events",
	"ingestion_batches",
	"legal_holds",
	"retention_destructions",
}

type Manifest struct {
	FormatVersion int       `json:"format_version"`
	CreatedAt     time.Time `json:"created_at"`
	Database      string    `json:"database"`
	SHA256        string    `json:"sha256"`
	SizeBytes     int64     `json:"size_bytes"`
	Tables        []string  `json:"required_tables"`
}

type DrillReport struct {
	Manifest      Manifest       `json:"manifest"`
	Integrity     string         `json:"integrity"`
	RestoredBytes int64          `json:"restored_bytes"`
	TableCounts   map[string]int `json:"table_counts"`
}

func Create(
	ctx context.Context,
	sourcePath string,
	outputDirectory string,
	now time.Time,
) (Manifest, string, error) {
	sourcePath, err := regularFile(sourcePath)
	if err != nil {
		return Manifest{}, "", fmt.Errorf("validate source database: %w", err)
	}
	outputDirectory, err = secureDirectory(outputDirectory)
	if err != nil {
		return Manifest{}, "", err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}

	temporary, err := os.CreateTemp(outputDirectory, ".telenexus-backup-*.db")
	if err != nil {
		return Manifest{}, "", fmt.Errorf("reserve backup path: %w", err)
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		return Manifest{}, "", fmt.Errorf("close reserved backup path: %w", err)
	}
	if err := os.Remove(temporaryPath); err != nil {
		return Manifest{}, "", fmt.Errorf("prepare backup path: %w", err)
	}
	defer func() { _ = os.Remove(temporaryPath) }()

	db, err := sql.Open("sqlite", sourcePath)
	if err != nil {
		return Manifest{}, "", fmt.Errorf("open source database: %w", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		return Manifest{}, "", fmt.Errorf("configure source database: %w", err)
	}
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", temporaryPath); err != nil {
		return Manifest{}, "", fmt.Errorf("create database snapshot: %w", err)
	}
	if err := db.Close(); err != nil {
		return Manifest{}, "", fmt.Errorf("close source database: %w", err)
	}
	if err := secureFile(temporaryPath); err != nil {
		return Manifest{}, "", err
	}
	if err := syncFile(temporaryPath); err != nil {
		return Manifest{}, "", err
	}
	if _, _, err := inspectDatabase(ctx, temporaryPath); err != nil {
		return Manifest{}, "", fmt.Errorf("validate backup snapshot: %w", err)
	}

	info, err := os.Stat(temporaryPath)
	if err != nil {
		return Manifest{}, "", fmt.Errorf("inspect backup snapshot: %w", err)
	}
	digest, err := fileSHA256(temporaryPath)
	if err != nil {
		return Manifest{}, "", err
	}
	name := "telenexus-" + now.Format("20060102T150405.000000000Z") + ".db"
	backupPath := filepath.Join(outputDirectory, name)
	if _, err := os.Lstat(backupPath); err == nil {
		return Manifest{}, "", fmt.Errorf("backup already exists: %s", backupPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Manifest{}, "", fmt.Errorf("inspect backup destination: %w", err)
	}
	if err := publishNoReplace(temporaryPath, backupPath); err != nil {
		return Manifest{}, "", fmt.Errorf("publish backup snapshot: %w", err)
	}

	manifest := Manifest{
		FormatVersion: FormatVersion,
		CreatedAt:     now,
		Database:      name,
		SHA256:        digest,
		SizeBytes:     info.Size(),
		Tables:        append([]string(nil), requiredTables...),
	}
	if err := writeManifest(manifest, ManifestPath(backupPath)); err != nil {
		_ = os.Remove(backupPath)
		return Manifest{}, "", err
	}
	return manifest, backupPath, nil
}

func Verify(ctx context.Context, backupPath string) (Manifest, error) {
	backupPath, err := regularFile(backupPath)
	if err != nil {
		return Manifest{}, fmt.Errorf("validate backup file: %w", err)
	}
	manifest, err := readManifest(ManifestPath(backupPath), backupPath)
	if err != nil {
		return Manifest{}, err
	}
	if err := verifyDatabase(ctx, backupPath, manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func Restore(ctx context.Context, backupPath, targetPath string) (Manifest, error) {
	backupPath, err := regularFile(backupPath)
	if err != nil {
		return Manifest{}, fmt.Errorf("validate backup file: %w", err)
	}
	manifest, err := readManifest(ManifestPath(backupPath), backupPath)
	if err != nil {
		return Manifest{}, err
	}
	targetPath, err = cleanTarget(targetPath)
	if err != nil {
		return Manifest{}, err
	}
	if _, err := os.Lstat(targetPath); err == nil {
		return Manifest{}, fmt.Errorf("restore target already exists: %s", targetPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Manifest{}, fmt.Errorf("inspect restore target: %w", err)
	}
	if _, err := secureDirectory(filepath.Dir(targetPath)); err != nil {
		return Manifest{}, fmt.Errorf("validate restore directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(targetPath), ".telenexus-restore-*")
	if err != nil {
		return Manifest{}, fmt.Errorf("create temporary restore file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return Manifest{}, fmt.Errorf("secure restore file: %w", err)
	}
	if err := copyFile(backupPath, temporary); err != nil {
		_ = temporary.Close()
		return Manifest{}, err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return Manifest{}, fmt.Errorf("sync restore file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return Manifest{}, fmt.Errorf("close restore file: %w", err)
	}
	if err := verifyDatabase(ctx, temporaryPath, manifest); err != nil {
		return Manifest{}, err
	}
	if _, err := secureDirectory(filepath.Dir(targetPath)); err != nil {
		return Manifest{}, fmt.Errorf("revalidate restore directory: %w", err)
	}
	if err := publishNoReplace(temporaryPath, targetPath); err != nil {
		return Manifest{}, fmt.Errorf("publish restored database: %w", err)
	}
	return manifest, nil
}

func Drill(ctx context.Context, backupPath string) (DrillReport, error) {
	backupPath, err := regularFile(backupPath)
	if err != nil {
		return DrillReport{}, fmt.Errorf("validate backup file: %w", err)
	}
	manifest, err := readManifest(ManifestPath(backupPath), backupPath)
	if err != nil {
		return DrillReport{}, err
	}
	temporaryDirectory, err := os.MkdirTemp("", "telenexus-restore-drill-*")
	if err != nil {
		return DrillReport{}, fmt.Errorf("create restore drill directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(temporaryDirectory) }()
	targetPath := filepath.Join(temporaryDirectory, "restored.db")
	if _, err := Restore(ctx, backupPath, targetPath); err != nil {
		return DrillReport{}, err
	}
	integrity, counts, err := inspectDatabase(ctx, targetPath)
	if err != nil {
		return DrillReport{}, err
	}
	info, err := os.Stat(targetPath)
	if err != nil {
		return DrillReport{}, fmt.Errorf("inspect restored database: %w", err)
	}
	return DrillReport{
		Manifest:      manifest,
		Integrity:     integrity,
		RestoredBytes: info.Size(),
		TableCounts:   counts,
	}, nil
}

func ManifestPath(backupPath string) string {
	return backupPath + ".manifest.json"
}

func verifyDatabase(ctx context.Context, path string, manifest Manifest) error {
	if manifest.FormatVersion != FormatVersion {
		return fmt.Errorf("unsupported backup format version %d", manifest.FormatVersion)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect backup database: %w", err)
	}
	if info.Size() != manifest.SizeBytes {
		return errors.New("backup database size does not match manifest")
	}
	digest, err := fileSHA256(path)
	if err != nil {
		return err
	}
	if digest != manifest.SHA256 {
		return errors.New("backup database SHA-256 does not match manifest")
	}
	integrity, _, err := inspectDatabase(ctx, path)
	if err != nil {
		return err
	}
	if integrity != "ok" {
		return fmt.Errorf("backup database integrity check failed: %s", integrity)
	}
	return nil
}

func inspectDatabase(
	ctx context.Context,
	path string,
) (string, map[string]int, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", nil, fmt.Errorf("resolve database path: %w", err)
	}
	dsn := (&url.URL{
		Scheme:   "file",
		Path:     filepath.ToSlash(absolute),
		RawQuery: "mode=ro",
	}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return "", nil, fmt.Errorf("open backup database: %w", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(ctx, "PRAGMA query_only = ON"); err != nil {
		return "", nil, fmt.Errorf("set backup database read-only: %w", err)
	}
	var integrity string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		return "", nil, fmt.Errorf("check backup database integrity: %w", err)
	}
	counts := make(map[string]int, len(requiredTables))
	for _, table := range requiredTables {
		var exists int
		if err := db.QueryRowContext(ctx, `
SELECT COUNT(1) FROM sqlite_master
WHERE type = 'table' AND name = ?`, table).Scan(&exists); err != nil {
			return "", nil, fmt.Errorf("inspect required table %s: %w", table, err)
		}
		if exists != 1 {
			return "", nil, fmt.Errorf("backup is missing required table %s", table)
		}
		var count int
		if err := db.QueryRowContext(
			ctx,
			"SELECT COUNT(1) FROM "+table,
		).Scan(&count); err != nil {
			return "", nil, fmt.Errorf("count required table %s: %w", table, err)
		}
		counts[table] = count
	}
	return integrity, counts, nil
}

func readManifest(path, backupPath string) (Manifest, error) {
	path, err := regularFile(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("validate backup manifest: %w", err)
	}
	file, err := os.Open(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("open backup manifest: %w", err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return Manifest{}, fmt.Errorf("inspect backup manifest: %w", err)
	}
	if info.Size() > maxManifest {
		return Manifest{}, errors.New("backup manifest is too large")
	}
	decoder := json.NewDecoder(io.LimitReader(file, maxManifest))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode backup manifest: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Manifest{}, errors.New("backup manifest contains trailing data")
	}
	if manifest.Database != filepath.Base(backupPath) ||
		len(manifest.SHA256) != sha256.Size*2 {
		return Manifest{}, errors.New("backup manifest does not match database file")
	}
	if _, err := hex.DecodeString(manifest.SHA256); err != nil {
		return Manifest{}, errors.New("backup manifest SHA-256 is invalid")
	}
	if manifest.CreatedAt.IsZero() || manifest.SizeBytes <= 0 {
		return Manifest{}, errors.New("backup manifest metadata is invalid")
	}
	if !equalStrings(manifest.Tables, requiredTables) {
		return Manifest{}, errors.New("backup manifest table list is invalid")
	}
	return manifest, nil
}

func writeManifest(manifest Manifest, path string) error {
	if _, err := secureDirectory(filepath.Dir(path)); err != nil {
		return fmt.Errorf("validate backup manifest directory: %w", err)
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode backup manifest: %w", err)
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), ".telenexus-manifest-*")
	if err != nil {
		return fmt.Errorf("create backup manifest: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure backup manifest: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write backup manifest: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync backup manifest: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close backup manifest: %w", err)
	}
	if _, err := secureDirectory(filepath.Dir(path)); err != nil {
		return fmt.Errorf("revalidate backup manifest directory: %w", err)
	}
	if err := publishNoReplace(temporaryPath, path); err != nil {
		return fmt.Errorf("publish backup manifest: %w", err)
	}
	return nil
}

func publishNoReplace(source, target string) error {
	if err := os.Link(source, target); err != nil {
		return err
	}
	_ = os.Remove(source)
	return nil
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func regularFile(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("file path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve file path: %w", err)
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", errors.New("path must be a regular file, not a symlink")
	}
	if err := validateDirectoryChain(filepath.Dir(absolute), false); err != nil {
		return "", err
	}
	return absolute, nil
}

func secureDirectory(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("backup output directory is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve backup output directory: %w", err)
	}
	if err := validateDirectoryChain(absolute, true); err != nil {
		return "", fmt.Errorf("validate backup output directory: %w", err)
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", fmt.Errorf("inspect backup output directory: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("backup output directory must not grant group or other access")
	}
	return absolute, nil
}

func validateDirectoryChain(path string, create bool) error {
	root := string(filepath.Separator)
	if volume := filepath.VolumeName(path); volume != "" {
		root = volume + string(filepath.Separator)
	}
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return fmt.Errorf("resolve directory path: %w", err)
	}
	current := root
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) && create {
			if err := os.Mkdir(current, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return fmt.Errorf("create secure directory: %w", err)
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return fmt.Errorf("inspect directory path: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if !trustedSystemDirectoryLink(current, info) {
				return errors.New("directory path must not contain symlinks")
			}
			info, err = os.Stat(current)
			if err != nil {
				return fmt.Errorf("inspect system directory link: %w", err)
			}
		}
		if !info.IsDir() {
			return errors.New("directory path contains a non-directory component")
		}
	}
	return nil
}

func trustedSystemDirectoryLink(path string, info os.FileInfo) bool {
	if runtime.GOOS != "darwin" || filepath.Dir(path) != string(filepath.Separator) {
		return false
	}
	expectedTargets := map[string]string{
		"/etc": "/private/etc",
		"/tmp": "/private/tmp",
		"/var": "/private/var",
	}
	expectedTarget, ok := expectedTargets[filepath.Clean(path)]
	if !ok {
		return false
	}
	target, err := os.Readlink(path)
	if err != nil {
		return false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(path), target)
	}
	return filepath.Clean(target) == expectedTarget &&
		info.Mode()&os.ModeSymlink != 0
}

func cleanTarget(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("restore target is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve restore target: %w", err)
	}
	return absolute, nil
}

func secureFile(path string) error {
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("secure backup file: %w", err)
	}
	return nil
}

func syncFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open backup file for sync: %w", err)
	}
	defer func() { _ = file.Close() }()
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync backup file: %w", err)
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open file for hashing: %w", err)
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("hash file: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func copyFile(source string, target *os.File) error {
	file, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open backup for restore: %w", err)
	}
	defer func() { _ = file.Close() }()
	if _, err := io.Copy(target, file); err != nil {
		return fmt.Errorf("copy backup for restore: %w", err)
	}
	return nil
}
