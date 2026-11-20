// Package operations stores account groups, monitor rules, alerts, and the message index.
package operations

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound               = errors.New("operations record not found")
	ErrActiveLegalHold        = errors.New("account has an active legal hold")
	ErrAlertInvalid           = errors.New("invalid monitor alert update")
	ErrAlertConflict          = errors.New("monitor alert state conflict")
	ErrProtectedChannel       = errors.New("built-in notification channel cannot be deleted")
	ErrNotificationChannelUse = errors.New("notification channel is used by monitor rules")
)

type Group struct {
	ID           int64     `json:"id"`
	Name         string    `json:"name"`
	Color        string    `json:"color"`
	AccountIDs   []int64   `json:"account_ids"`
	AccountCount int       `json:"account_count"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type Rule struct {
	ID                      int64      `json:"id"`
	Name                    string     `json:"name"`
	AccountID               int64      `json:"account_id"`
	PeerType                string     `json:"peer_type"`
	PeerID                  int64      `json:"peer_id"`
	PeerTitle               string     `json:"peer_title"`
	Keywords                []string   `json:"keywords"`
	ExcludeKeywords         []string   `json:"exclude_keywords"`
	CooldownSeconds         int        `json:"cooldown_seconds"`
	NotificationChannelID   int64      `json:"notification_channel_id"`
	NotificationChannelName string     `json:"notification_channel_name"`
	NotificationChannelKind string     `json:"notification_channel_kind"`
	Owner                   string     `json:"owner"`
	Version                 int        `json:"version"`
	CreatedBy               string     `json:"created_by"`
	Enabled                 bool       `json:"enabled"`
	LastMessageID           int        `json:"last_message_id"`
	LastRunAt               *time.Time `json:"last_run_at,omitempty"`
	LastError               string     `json:"last_error,omitempty"`
	CreatedAt               time.Time  `json:"created_at"`
	UpdatedAt               time.Time  `json:"updated_at"`
}

type RuleCreateInput struct {
	Name                  string
	AccountID             int64
	PeerType              string
	PeerID                int64
	PeerTitle             string
	NotificationChannelID int64
	Keywords              []string
	ExcludeKeywords       []string
	CooldownSeconds       int
	Owner                 string
	Actor                 string
}

type RuleUpdateInput struct {
	Name                  *string
	AccountID             *int64
	PeerType              *string
	PeerID                *int64
	PeerTitle             *string
	NotificationChannelID *int64
	Keywords              *[]string
	ExcludeKeywords       *[]string
	CooldownSeconds       *int
	Owner                 *string
	Enabled               *bool
	Actor                 string
}

type RuleVersion struct {
	RuleID                int64     `json:"rule_id"`
	Version               int       `json:"version"`
	Name                  string    `json:"name"`
	AccountID             int64     `json:"account_id"`
	PeerType              string    `json:"peer_type"`
	PeerID                int64     `json:"peer_id"`
	PeerTitle             string    `json:"peer_title"`
	Keywords              []string  `json:"keywords"`
	ExcludeKeywords       []string  `json:"exclude_keywords"`
	CooldownSeconds       int       `json:"cooldown_seconds"`
	NotificationChannelID int64     `json:"notification_channel_id"`
	Owner                 string    `json:"owner"`
	Enabled               bool      `json:"enabled"`
	Actor                 string    `json:"actor"`
	CreatedAt             time.Time `json:"created_at"`
}

type RulePreviewInput struct {
	AccountID       int64
	PeerType        string
	PeerID          int64
	Keywords        []string
	ExcludeKeywords []string
	Limit           int
}

type RulePreviewSample struct {
	MessageID      int       `json:"message_id"`
	Sender         string    `json:"sender"`
	Text           string    `json:"text"`
	MatchedKeyword string    `json:"matched_keyword,omitempty"`
	ExcludedBy     string    `json:"excluded_by,omitempty"`
	SentAt         time.Time `json:"sent_at"`
}

type RulePreview struct {
	Scanned       int                 `json:"scanned"`
	Matched       int                 `json:"matched"`
	Excluded      int                 `json:"excluded"`
	Samples       []RulePreviewSample `json:"samples"`
	ExcludedItems []RulePreviewSample `json:"excluded_items"`
}

type NotificationChannel struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	Kind       string    `json:"kind"`
	Enabled    bool      `json:"enabled"`
	Configured bool      `json:"configured"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type Alert struct {
	ID             int64      `json:"id"`
	RuleID         int64      `json:"rule_id"`
	RuleName       string     `json:"rule_name"`
	RuleVersion    int        `json:"rule_version"`
	AccountID      int64      `json:"account_id"`
	PeerType       string     `json:"peer_type"`
	PeerID         int64      `json:"peer_id"`
	PeerTitle      string     `json:"peer_title"`
	MessageID      int        `json:"message_id"`
	MatchedKeyword string     `json:"matched_keyword,omitempty"`
	MessagePreview string     `json:"message_preview"`
	State          string     `json:"state"`
	ChannelID      int64      `json:"notification_channel_id"`
	ChannelName    string     `json:"notification_channel_name"`
	DeliveryID     int64      `json:"delivery_id,omitempty"`
	DeliveryState  string     `json:"delivery_state,omitempty"`
	DeliveryCount  int        `json:"delivery_attempts"`
	DeliveryLastAt *time.Time `json:"delivery_last_attempt_at,omitempty"`
	DeliveryAt     *time.Time `json:"delivery_at,omitempty"`
	DeliveryNextAt *time.Time `json:"delivery_next_attempt_at,omitempty"`
	DeliveryError  string     `json:"delivery_error,omitempty"`
	Severity       string     `json:"severity"`
	AssignedTo     string     `json:"assigned_to,omitempty"`
	AcknowledgedBy string     `json:"acknowledged_by,omitempty"`
	AcknowledgedAt *time.Time `json:"acknowledged_at,omitempty"`
	ResolvedBy     string     `json:"resolved_by,omitempty"`
	ResolvedAt     *time.Time `json:"resolved_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type AlertUpdateInput struct {
	Action   string `json:"action"`
	Severity string `json:"severity"`
	Assignee string `json:"assignee"`
	Note     string `json:"note"`
	Actor    string `json:"-"`
}

type AlertEvent struct {
	Sequence      int64     `json:"sequence"`
	AlertID       int64     `json:"alert_id"`
	Action        string    `json:"action"`
	Actor         string    `json:"actor"`
	Note          string    `json:"note,omitempty"`
	PreviousState string    `json:"previous_state"`
	NextState     string    `json:"next_state"`
	CreatedAt     time.Time `json:"created_at"`
}

type IndexedMessage struct {
	AccountID     int64     `json:"account_id"`
	PeerType      string    `json:"peer_type"`
	PeerID        int64     `json:"peer_id"`
	PeerTitle     string    `json:"peer_title"`
	MessageID     int       `json:"message_id"`
	Sender        string    `json:"sender"`
	Text          string    `json:"text"`
	SentAt        time.Time `json:"sent_at"`
	IndexedAt     time.Time `json:"indexed_at"`
	LatestEventID string    `json:"latest_event_id,omitempty"`
	ContentHash   string    `json:"content_hash,omitempty"`
	SchemaVersion int       `json:"schema_version"`
}

type MessageQuery struct {
	Query     string
	AccountID int64
	PeerType  string
	From      time.Time
	To        time.Time
	Limit     int
	Offset    int
}

type MessagePage struct {
	Items  []IndexedMessage `json:"items"`
	Total  int              `json:"total"`
	Limit  int              `json:"limit"`
	Offset int              `json:"offset"`
}

type DailyCount struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
}

type AccountCount struct {
	AccountID int64 `json:"account_id"`
	Count     int   `json:"count"`
}

type Report struct {
	Days        int            `json:"days"`
	Total       int            `json:"total"`
	ByDay       []DailyCount   `json:"by_day"`
	ByAccount   []AccountCount `json:"by_account"`
	GeneratedAt time.Time      `json:"generated_at"`
}

type Dashboard struct {
	AccountCount  int  `json:"account_count"`
	OnlineCount   int  `json:"online_count"`
	MonitorCount  int  `json:"monitor_count"`
	TodayMessages int  `json:"today_messages"`
	PendingAlerts int  `json:"pending_alerts"`
	TelegramReady bool `json:"telegram_ready"`
}

type Service struct {
	db                        *sql.DB
	now                       func() time.Time
	searchIndexMu             sync.Mutex
	searchIndexLastRebuild    time.Time
	searchIndexRebuildCount   int
	searchIndexLastRebuildErr string
}

func Open(ctx context.Context, path string) (*Service, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("operations database path must not be empty")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open operations database: %w", err)
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA busy_timeout = 5000",
		"PRAGMA journal_mode = WAL",
	} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("configure operations database: %w", err)
		}
	}
	service := &Service{db: db, now: time.Now}
	if err := service.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("secure operations database: %w", err)
	}
	return service, nil
}

func (s *Service) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close operations database: %w", err)
	}
	return nil
}

func (s *Service) migrate(ctx context.Context) error {
	var ftsExists int
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(1) FROM sqlite_master
WHERE type = 'table' AND name = 'indexed_messages_fts'`).Scan(&ftsExists); err != nil {
		return fmt.Errorf("inspect message search index: %w", err)
	}
	const schema = `
CREATE TABLE IF NOT EXISTS account_groups (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL COLLATE NOCASE UNIQUE,
	color TEXT NOT NULL DEFAULT 'blue',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS account_group_members (
	group_id INTEGER NOT NULL,
	account_id INTEGER NOT NULL,
	added_at TEXT NOT NULL,
	PRIMARY KEY (group_id, account_id)
);
CREATE INDEX IF NOT EXISTS account_group_members_account_idx
	ON account_group_members(account_id, group_id);
CREATE TABLE IF NOT EXISTS notification_channels (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL COLLATE NOCASE UNIQUE,
	kind TEXT NOT NULL,
	enabled INTEGER NOT NULL DEFAULT 1,
	config_json TEXT NOT NULL DEFAULT '{}',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS monitor_rules (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL,
	account_id INTEGER NOT NULL,
	peer_type TEXT NOT NULL,
	peer_id INTEGER NOT NULL,
	peer_title TEXT NOT NULL DEFAULT '',
	keywords_json TEXT NOT NULL DEFAULT '[]',
	exclude_keywords_json TEXT NOT NULL DEFAULT '[]',
	cooldown_seconds INTEGER NOT NULL DEFAULT 0,
	notification_channel_id INTEGER NOT NULL DEFAULT 1,
	owner TEXT NOT NULL DEFAULT '',
	version INTEGER NOT NULL DEFAULT 1,
	created_by TEXT NOT NULL DEFAULT '',
	enabled INTEGER NOT NULL DEFAULT 1,
	last_message_id INTEGER NOT NULL DEFAULT 0,
	last_run_at TEXT NOT NULL DEFAULT '',
	last_error TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS monitor_rules_enabled_idx
	ON monitor_rules(enabled, account_id, id);
CREATE TABLE IF NOT EXISTS monitor_rule_versions (
	rule_id INTEGER NOT NULL,
	version INTEGER NOT NULL,
	name TEXT NOT NULL,
	account_id INTEGER NOT NULL,
	peer_type TEXT NOT NULL,
	peer_id INTEGER NOT NULL,
	peer_title TEXT NOT NULL DEFAULT '',
	keywords_json TEXT NOT NULL DEFAULT '[]',
	exclude_keywords_json TEXT NOT NULL DEFAULT '[]',
	cooldown_seconds INTEGER NOT NULL DEFAULT 0,
	notification_channel_id INTEGER NOT NULL DEFAULT 1,
	owner TEXT NOT NULL DEFAULT '',
	enabled INTEGER NOT NULL DEFAULT 1,
	actor TEXT NOT NULL,
	created_at TEXT NOT NULL,
	PRIMARY KEY (rule_id, version)
);
CREATE INDEX IF NOT EXISTS monitor_rule_versions_rule_idx
	ON monitor_rule_versions(rule_id, version DESC);
CREATE TRIGGER IF NOT EXISTS monitor_rule_versions_no_update
BEFORE UPDATE ON monitor_rule_versions
BEGIN
	SELECT RAISE(ABORT, 'monitor rule versions are append-only');
END;
CREATE TRIGGER IF NOT EXISTS monitor_rule_versions_no_delete
BEFORE DELETE ON monitor_rule_versions
BEGIN
	SELECT RAISE(ABORT, 'monitor rule versions are append-only');
END;
CREATE TABLE IF NOT EXISTS monitor_alerts (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	rule_id INTEGER NOT NULL,
	rule_version INTEGER NOT NULL DEFAULT 1,
	account_id INTEGER NOT NULL,
	peer_type TEXT NOT NULL,
	peer_id INTEGER NOT NULL,
	peer_title TEXT NOT NULL DEFAULT '',
	message_id INTEGER NOT NULL,
	matched_keyword TEXT NOT NULL DEFAULT '',
	message_preview TEXT NOT NULL DEFAULT '',
	state TEXT NOT NULL DEFAULT 'pending',
	severity TEXT NOT NULL DEFAULT 'medium',
	assigned_to TEXT NOT NULL DEFAULT '',
	acknowledged_by TEXT NOT NULL DEFAULT '',
	acknowledged_at TEXT NOT NULL DEFAULT '',
	resolved_by TEXT NOT NULL DEFAULT '',
	resolved_at TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	UNIQUE(rule_id, message_id)
);
CREATE INDEX IF NOT EXISTS monitor_alerts_state_created_idx
	ON monitor_alerts(state, created_at DESC, id DESC);
CREATE TABLE IF NOT EXISTS notification_deliveries (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	alert_id INTEGER NOT NULL,
	channel_id INTEGER NOT NULL,
	channel_kind TEXT NOT NULL,
	state TEXT NOT NULL CHECK (state IN ('queued', 'delivered', 'failed')),
	attempt_count INTEGER NOT NULL DEFAULT 0,
	last_attempt_at TEXT NOT NULL DEFAULT '',
	delivered_at TEXT NOT NULL DEFAULT '',
	next_attempt_at TEXT NOT NULL DEFAULT '',
	error_text TEXT NOT NULL DEFAULT '',
	failure_alerted_at TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	UNIQUE(alert_id, channel_id)
);
CREATE INDEX IF NOT EXISTS notification_deliveries_state_created_idx
	ON notification_deliveries(state, created_at DESC, id DESC);
CREATE TABLE IF NOT EXISTS monitor_alert_events (
	sequence INTEGER PRIMARY KEY AUTOINCREMENT,
	alert_id INTEGER NOT NULL,
	action TEXT NOT NULL CHECK (
		action IN (
			'created', 'acknowledged', 'assigned', 'severity_changed',
			'noted', 'resolved', 'reopened'
		)
	),
	actor TEXT NOT NULL,
	note TEXT NOT NULL DEFAULT '',
	previous_state TEXT NOT NULL,
	next_state TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS monitor_alert_events_alert_idx
	ON monitor_alert_events(alert_id, sequence DESC);
CREATE TABLE IF NOT EXISTS indexed_messages (
	account_id INTEGER NOT NULL,
	peer_type TEXT NOT NULL,
	peer_id INTEGER NOT NULL,
	peer_title TEXT NOT NULL DEFAULT '',
	message_id INTEGER NOT NULL,
	sender TEXT NOT NULL DEFAULT '',
	text TEXT NOT NULL DEFAULT '',
	sent_at TEXT NOT NULL,
	indexed_at TEXT NOT NULL,
	PRIMARY KEY (account_id, peer_type, peer_id, message_id)
);
CREATE INDEX IF NOT EXISTS indexed_messages_sent_idx
	ON indexed_messages(sent_at DESC, account_id, message_id);
CREATE INDEX IF NOT EXISTS indexed_messages_peer_idx
	ON indexed_messages(account_id, peer_type, peer_id, message_id DESC);
CREATE VIRTUAL TABLE IF NOT EXISTS indexed_messages_fts USING fts5(
	text,
	sender,
	peer_title,
	content = 'indexed_messages',
	content_rowid = 'rowid',
	tokenize = 'trigram'
);
CREATE TRIGGER IF NOT EXISTS indexed_messages_fts_insert
AFTER INSERT ON indexed_messages
BEGIN
	INSERT INTO indexed_messages_fts(rowid, text, sender, peer_title)
	VALUES (new.rowid, new.text, new.sender, new.peer_title);
END;
CREATE TRIGGER IF NOT EXISTS indexed_messages_fts_delete
AFTER DELETE ON indexed_messages
BEGIN
	INSERT INTO indexed_messages_fts(
		indexed_messages_fts, rowid, text, sender, peer_title
	) VALUES ('delete', old.rowid, old.text, old.sender, old.peer_title);
END;
CREATE TRIGGER IF NOT EXISTS indexed_messages_fts_update
AFTER UPDATE ON indexed_messages
BEGIN
	INSERT INTO indexed_messages_fts(
		indexed_messages_fts, rowid, text, sender, peer_title
	) VALUES ('delete', old.rowid, old.text, old.sender, old.peer_title);
	INSERT INTO indexed_messages_fts(rowid, text, sender, peer_title)
	VALUES (new.rowid, new.text, new.sender, new.peer_title);
END;
CREATE TABLE IF NOT EXISTS monitor_checkpoints (
	account_id INTEGER NOT NULL,
	peer_type TEXT NOT NULL,
	peer_id INTEGER NOT NULL,
	last_message_id INTEGER NOT NULL DEFAULT 0,
	last_success_at TEXT NOT NULL DEFAULT '',
	consecutive_failures INTEGER NOT NULL DEFAULT 0,
	last_error TEXT NOT NULL DEFAULT '',
	backlog_detected INTEGER NOT NULL DEFAULT 0,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (account_id, peer_type, peer_id)
);
CREATE TABLE IF NOT EXISTS monitor_account_states (
	account_id INTEGER PRIMARY KEY,
	next_collection_at TEXT NOT NULL DEFAULT '',
	consecutive_failures INTEGER NOT NULL DEFAULT 0,
	last_error TEXT NOT NULL DEFAULT '',
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS ingestion_batches (
	batch_id TEXT PRIMARY KEY,
	source TEXT NOT NULL,
	account_id INTEGER NOT NULL,
	peer_type TEXT NOT NULL,
	peer_id INTEGER NOT NULL,
	status TEXT NOT NULL CHECK (status IN ('completed', 'partial', 'failed')),
	started_at TEXT NOT NULL,
	finished_at TEXT NOT NULL,
	fetched_count INTEGER NOT NULL DEFAULT 0,
	event_count INTEGER NOT NULL DEFAULT 0,
	inserted_count INTEGER NOT NULL DEFAULT 0,
	checkpoint_before INTEGER NOT NULL DEFAULT 0,
	checkpoint_after INTEGER NOT NULL DEFAULT 0,
	backlog_detected INTEGER NOT NULL DEFAULT 0,
	payload_sha256 TEXT NOT NULL DEFAULT '',
	error_text TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS ingestion_batches_target_idx
	ON ingestion_batches(account_id, peer_type, peer_id, finished_at DESC);
CREATE TRIGGER IF NOT EXISTS ingestion_batches_no_update
BEFORE UPDATE ON ingestion_batches
BEGIN
	SELECT RAISE(ABORT, 'ingestion batches are append-only');
END;
CREATE TABLE IF NOT EXISTS evidence_events (
	sequence INTEGER PRIMARY KEY AUTOINCREMENT,
	event_id TEXT NOT NULL UNIQUE,
	batch_id TEXT NOT NULL,
	account_id INTEGER NOT NULL,
	peer_type TEXT NOT NULL,
	peer_id INTEGER NOT NULL,
	peer_title TEXT NOT NULL DEFAULT '',
	message_id INTEGER NOT NULL,
	event_type TEXT NOT NULL CHECK (event_type IN ('message', 'edit', 'delete')),
	version INTEGER NOT NULL,
	actor TEXT NOT NULL DEFAULT '',
	sender TEXT NOT NULL DEFAULT '',
	text TEXT NOT NULL DEFAULT '',
	media_json TEXT NOT NULL DEFAULT '{}',
	raw_json TEXT NOT NULL DEFAULT '{}',
	sent_at TEXT NOT NULL DEFAULT '',
	observed_at TEXT NOT NULL,
	content_sha256 TEXT NOT NULL,
	schema_version INTEGER NOT NULL,
	UNIQUE(account_id, peer_type, peer_id, message_id, version)
);
CREATE INDEX IF NOT EXISTS evidence_events_message_idx
	ON evidence_events(account_id, peer_type, peer_id, message_id, version DESC);
CREATE INDEX IF NOT EXISTS evidence_events_batch_idx
	ON evidence_events(batch_id, sequence);
CREATE TRIGGER IF NOT EXISTS evidence_events_no_update
BEFORE UPDATE ON evidence_events
BEGIN
	SELECT RAISE(ABORT, 'evidence events are append-only');
END;
CREATE TABLE IF NOT EXISTS retention_policies (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	message_retention_days INTEGER NOT NULL
		CHECK (message_retention_days BETWEEN 1 AND 3650),
	version INTEGER NOT NULL DEFAULT 1,
	updated_by TEXT NOT NULL DEFAULT 'system',
	updated_at TEXT NOT NULL
);
INSERT OR IGNORE INTO retention_policies (
	id, message_retention_days, version, updated_by, updated_at
) VALUES (
	1, 30, 1, 'system',
	strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
);
CREATE TABLE IF NOT EXISTS legal_holds (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	scope TEXT NOT NULL CHECK (scope IN ('account', 'chat', 'message')),
	account_id INTEGER NOT NULL,
	peer_type TEXT NOT NULL DEFAULT '',
	peer_id INTEGER NOT NULL DEFAULT 0,
	message_id INTEGER NOT NULL DEFAULT 0,
	reason TEXT NOT NULL,
	created_by TEXT NOT NULL,
	created_at TEXT NOT NULL,
	released_by TEXT NOT NULL DEFAULT '',
	released_at TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX IF NOT EXISTS legal_holds_active_scope_idx
	ON legal_holds(scope, account_id, peer_type, peer_id, message_id)
	WHERE released_at = '';
CREATE INDEX IF NOT EXISTS legal_holds_account_idx
	ON legal_holds(account_id, released_at);
CREATE TABLE IF NOT EXISTS retention_destructions (
	sequence INTEGER PRIMARY KEY AUTOINCREMENT,
	account_id INTEGER NOT NULL,
	peer_type TEXT NOT NULL,
	peer_id INTEGER NOT NULL,
	message_id INTEGER NOT NULL,
	content_hash TEXT NOT NULL DEFAULT '',
	evidence_event_count INTEGER NOT NULL,
	retention_days INTEGER NOT NULL,
	destroyed_by TEXT NOT NULL,
	reason TEXT NOT NULL,
	destroyed_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS retention_destructions_time_idx
	ON retention_destructions(destroyed_at DESC, sequence DESC);
CREATE TRIGGER IF NOT EXISTS retention_destructions_no_update
BEFORE UPDATE ON retention_destructions
BEGIN
	SELECT RAISE(ABORT, 'retention destruction records are append-only');
END;
CREATE TRIGGER IF NOT EXISTS retention_destructions_no_delete
BEFORE DELETE ON retention_destructions
BEGIN
	SELECT RAISE(ABORT, 'retention destruction records are append-only');
END;
CREATE TABLE IF NOT EXISTS account_destructions (
	sequence INTEGER PRIMARY KEY AUTOINCREMENT,
	account_id INTEGER NOT NULL UNIQUE,
	destroyed_at TEXT NOT NULL,
	reason TEXT NOT NULL,
	evidence_event_count INTEGER NOT NULL DEFAULT 0,
	ingestion_batch_count INTEGER NOT NULL DEFAULT 0,
	content_digest TEXT NOT NULL DEFAULT '',
	batch_digest TEXT NOT NULL DEFAULT '',
	schema_version INTEGER NOT NULL DEFAULT 1
);
CREATE TRIGGER IF NOT EXISTS account_destructions_no_update
BEFORE UPDATE ON account_destructions
BEGIN
	SELECT RAISE(ABORT, 'account destruction records are append-only');
END;
CREATE TRIGGER IF NOT EXISTS account_destructions_no_delete
BEFORE DELETE ON account_destructions
BEGIN
	SELECT RAISE(ABORT, 'account destruction records are append-only');
END;
CREATE TABLE IF NOT EXISTS pending_message_deletions (
	account_id INTEGER NOT NULL,
	peer_type TEXT NOT NULL,
	peer_id INTEGER NOT NULL,
	message_id INTEGER NOT NULL,
	observed_at TEXT NOT NULL,
	state TEXT NOT NULL DEFAULT 'pending'
		CHECK (state IN ('pending', 'ambiguous', 'missing')),
	attempts INTEGER NOT NULL DEFAULT 0,
	last_attempt_at TEXT NOT NULL DEFAULT '',
	last_error TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (account_id, peer_type, peer_id, message_id)
);
CREATE INDEX IF NOT EXISTS pending_message_deletions_state_idx
	ON pending_message_deletions(state, observed_at, account_id, message_id);
CREATE TABLE IF NOT EXISTS audit_events (
	sequence INTEGER PRIMARY KEY AUTOINCREMENT,
	request_id TEXT NOT NULL UNIQUE,
	actor TEXT NOT NULL,
	role TEXT NOT NULL,
	method TEXT NOT NULL,
	route TEXT NOT NULL,
	resource_type TEXT NOT NULL DEFAULT '',
	resource_id TEXT NOT NULL DEFAULT '',
	client_ip TEXT NOT NULL DEFAULT '',
	status_code INTEGER NOT NULL,
	outcome TEXT NOT NULL CHECK (outcome IN ('success', 'denied', 'failure')),
	occurred_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS audit_events_occurred_idx
	ON audit_events(occurred_at DESC, sequence DESC);
CREATE INDEX IF NOT EXISTS audit_events_actor_idx
	ON audit_events(actor, occurred_at DESC);
CREATE TRIGGER IF NOT EXISTS audit_events_no_update
BEFORE UPDATE ON audit_events
BEGIN
	SELECT RAISE(ABORT, 'audit events are append-only');
END;
CREATE TRIGGER IF NOT EXISTS audit_events_no_delete
BEFORE DELETE ON audit_events
BEGIN
	SELECT RAISE(ABORT, 'audit events are append-only');
END;`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate operations database: %w", err)
	}
	if ftsExists == 0 {
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO indexed_messages_fts(indexed_messages_fts) VALUES('rebuild')`,
		); err != nil {
			return fmt.Errorf("build message search index: %w", err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `
INSERT OR IGNORE INTO notification_channels (
	id, name, kind, enabled, config_json, created_at, updated_at
) VALUES (
	1, '站内通知', 'in_app', 1, '{}',
	strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
	strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
)`); err != nil {
		return fmt.Errorf("seed notification channel: %w", err)
	}
	if err := s.ensureColumn(
		ctx,
		"monitor_rules",
		"notification_channel_id",
		"notification_channel_id INTEGER NOT NULL DEFAULT 1",
	); err != nil {
		return err
	}
	if err := s.ensureColumn(
		ctx,
		"monitor_rules",
		"cooldown_seconds",
		"cooldown_seconds INTEGER NOT NULL DEFAULT 0",
	); err != nil {
		return err
	}
	for _, column := range []struct {
		name       string
		definition string
	}{
		{name: "exclude_keywords_json", definition: "exclude_keywords_json TEXT NOT NULL DEFAULT '[]'"},
		{name: "owner", definition: "owner TEXT NOT NULL DEFAULT ''"},
		{name: "version", definition: "version INTEGER NOT NULL DEFAULT 1"},
		{name: "created_by", definition: "created_by TEXT NOT NULL DEFAULT ''"},
	} {
		if err := s.ensureColumn(ctx, "monitor_rules", column.name, column.definition); err != nil {
			return err
		}
	}
	if _, err := s.db.ExecContext(ctx, `
UPDATE monitor_rules
SET owner = CASE WHEN owner = '' THEN 'system' ELSE owner END,
	created_by = CASE WHEN created_by = '' THEN 'system' ELSE created_by END
WHERE owner = '' OR created_by = ''`); err != nil {
		return fmt.Errorf("backfill monitor rule ownership: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
INSERT OR IGNORE INTO monitor_rule_versions (
	rule_id, version, name, account_id, peer_type, peer_id, peer_title,
	keywords_json, exclude_keywords_json, cooldown_seconds,
	notification_channel_id, owner, enabled, actor, created_at
)
SELECT id, version, name, account_id, peer_type, peer_id, peer_title,
	keywords_json, exclude_keywords_json, cooldown_seconds,
	notification_channel_id, owner, enabled, created_by, updated_at
FROM monitor_rules`); err != nil {
		return fmt.Errorf("backfill monitor rule versions: %w", err)
	}
	for _, column := range []struct {
		name       string
		definition string
	}{
		{name: "rule_version", definition: "rule_version INTEGER NOT NULL DEFAULT 1"},
		{name: "severity", definition: "severity TEXT NOT NULL DEFAULT 'medium'"},
		{name: "assigned_to", definition: "assigned_to TEXT NOT NULL DEFAULT ''"},
		{name: "acknowledged_by", definition: "acknowledged_by TEXT NOT NULL DEFAULT ''"},
		{name: "acknowledged_at", definition: "acknowledged_at TEXT NOT NULL DEFAULT ''"},
		{name: "resolved_by", definition: "resolved_by TEXT NOT NULL DEFAULT ''"},
		{name: "resolved_at", definition: "resolved_at TEXT NOT NULL DEFAULT ''"},
		{name: "updated_at", definition: "updated_at TEXT NOT NULL DEFAULT ''"},
	} {
		if err := s.ensureColumn(ctx, "monitor_alerts", column.name, column.definition); err != nil {
			return err
		}
	}
	if err := s.ensureColumn(
		ctx,
		"notification_deliveries",
		"next_attempt_at",
		"next_attempt_at TEXT NOT NULL DEFAULT ''",
	); err != nil {
		return err
	}
	if err := s.ensureColumn(
		ctx,
		"notification_deliveries",
		"failure_alerted_at",
		"failure_alerted_at TEXT NOT NULL DEFAULT ''",
	); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `
CREATE INDEX IF NOT EXISTS notification_deliveries_retry_idx
	ON notification_deliveries(state, next_attempt_at, id)`); err != nil {
		return fmt.Errorf("create notification retry index: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
UPDATE monitor_alerts SET updated_at = created_at WHERE updated_at = ''`); err != nil {
		return fmt.Errorf("backfill monitor alert update time: %w", err)
	}
	for _, column := range []struct {
		name       string
		definition string
	}{
		{name: "latest_event_id", definition: "latest_event_id TEXT NOT NULL DEFAULT ''"},
		{name: "content_hash", definition: "content_hash TEXT NOT NULL DEFAULT ''"},
		{name: "schema_version", definition: "schema_version INTEGER NOT NULL DEFAULT 1"},
		{name: "deleted_at", definition: "deleted_at TEXT NOT NULL DEFAULT ''"},
		{name: "updated_at", definition: "updated_at TEXT NOT NULL DEFAULT ''"},
	} {
		if err := s.ensureColumn(ctx, "indexed_messages", column.name, column.definition); err != nil {
			return err
		}
	}
	if err := s.ensureColumn(
		ctx,
		"ingestion_batches",
		"payload_sha256",
		"payload_sha256 TEXT NOT NULL DEFAULT ''",
	); err != nil {
		return err
	}
	for _, column := range []struct {
		name       string
		definition string
	}{
		{
			name:       "evidence_event_count",
			definition: "evidence_event_count INTEGER NOT NULL DEFAULT 0",
		},
		{
			name:       "ingestion_batch_count",
			definition: "ingestion_batch_count INTEGER NOT NULL DEFAULT 0",
		},
		{
			name:       "content_digest",
			definition: "content_digest TEXT NOT NULL DEFAULT ''",
		},
		{
			name:       "batch_digest",
			definition: "batch_digest TEXT NOT NULL DEFAULT ''",
		},
		{
			name:       "schema_version",
			definition: "schema_version INTEGER NOT NULL DEFAULT 1",
		},
	} {
		if err := s.ensureColumn(
			ctx,
			"account_destructions",
			column.name,
			column.definition,
		); err != nil {
			return err
		}
	}
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO monitor_checkpoints (
	account_id, peer_type, peer_id, last_message_id, updated_at
)
SELECT account_id, peer_type, peer_id, MAX(last_message_id),
	strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
FROM monitor_rules
GROUP BY account_id, peer_type, peer_id
HAVING MAX(last_message_id) > 0
ON CONFLICT(account_id, peer_type, peer_id) DO NOTHING`); err != nil {
		return fmt.Errorf("seed monitor checkpoints: %w", err)
	}
	return nil
}

func (s *Service) ensureColumn(
	ctx context.Context,
	table string,
	column string,
	definition string,
) error {
	rows, err := s.db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return fmt.Errorf("inspect %s schema: %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			index      int
			name       string
			columnType string
			notNull    int
			defaultVal sql.NullString
			primaryKey int
		)
		if err := rows.Scan(
			&index,
			&name,
			&columnType,
			&notNull,
			&defaultVal,
			&primaryKey,
		); err != nil {
			return fmt.Errorf("scan %s schema: %w", table, err)
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate %s schema: %w", table, err)
	}
	if _, err := s.db.ExecContext(
		ctx,
		"ALTER TABLE "+table+" ADD COLUMN "+definition,
	); err != nil {
		return fmt.Errorf("add %s.%s column: %w", table, column, err)
	}
	return nil
}

func (s *Service) CreateGroup(ctx context.Context, name, color string) (Group, error) {
	name = strings.TrimSpace(name)
	color = normalizeColor(color)
	if utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 40 {
		return Group{}, errors.New("group name must contain 1 to 40 characters")
	}
	now := s.now().UTC()
	result, err := s.db.ExecContext(
		ctx,
		`INSERT INTO account_groups (name, color, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		name,
		color,
		now.Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano),
	)
	if err != nil {
		return Group{}, fmt.Errorf("create account group: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Group{}, fmt.Errorf("read account group ID: %w", err)
	}
	return Group{
		ID:         id,
		Name:       name,
		Color:      color,
		AccountIDs: []int64{},
		CreatedAt:  now,
		UpdatedAt:  now,
	}, nil
}

func (s *Service) ListGroups(ctx context.Context) ([]Group, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, name, color, created_at, updated_at
FROM account_groups
ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list account groups: %w", err)
	}
	defer func() { _ = rows.Close() }()
	groups := make([]Group, 0)
	for rows.Next() {
		group, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		groups = append(groups, group)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate account groups: %w", err)
	}
	for index := range groups {
		accountIDs, err := s.groupAccountIDs(ctx, groups[index].ID)
		if err != nil {
			return nil, err
		}
		groups[index].AccountIDs = accountIDs
		groups[index].AccountCount = len(accountIDs)
	}
	return groups, nil
}

func (s *Service) DeleteGroup(ctx context.Context, id int64) error {
	if id <= 0 {
		return errors.New("group ID must be positive")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin delete account group: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM account_group_members WHERE group_id = ?`, id); err != nil {
		return fmt.Errorf("delete account group members: %w", err)
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM account_groups WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete account group: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check deleted account group: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit delete account group: %w", err)
	}
	return nil
}

func (s *Service) SetGroupAccounts(ctx context.Context, groupID int64, accountIDs []int64) error {
	if groupID <= 0 {
		return errors.New("group ID must be positive")
	}
	for _, accountID := range accountIDs {
		if accountID <= 0 {
			return errors.New("account IDs must be positive")
		}
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin update account group: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM account_groups WHERE id = ?`, groupID).
		Scan(&exists); err != nil {
		return fmt.Errorf("find account group: %w", err)
	}
	if exists == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM account_group_members WHERE group_id = ?`, groupID); err != nil {
		return fmt.Errorf("clear account group members: %w", err)
	}
	seen := make(map[int64]struct{}, len(accountIDs))
	for _, accountID := range accountIDs {
		if _, duplicate := seen[accountID]; duplicate {
			continue
		}
		seen[accountID] = struct{}{}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO account_group_members (group_id, account_id, added_at) VALUES (?, ?, ?)`,
			groupID,
			accountID,
			now,
		); err != nil {
			return fmt.Errorf("assign account %d to group: %w", accountID, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE account_groups SET updated_at = ? WHERE id = ?`, now, groupID); err != nil {
		return fmt.Errorf("touch account group: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit account group: %w", err)
	}
	return nil
}

func (s *Service) CreateRule(
	ctx context.Context,
	name string,
	accountID int64,
	peerType string,
	peerID int64,
	peerTitle string,
	keywords []string,
) (Rule, error) {
	return s.CreateRuleWithChannel(
		ctx,
		name,
		accountID,
		peerType,
		peerID,
		peerTitle,
		1,
		keywords,
	)
}

func (s *Service) CreateRuleWithChannel(
	ctx context.Context,
	name string,
	accountID int64,
	peerType string,
	peerID int64,
	peerTitle string,
	notificationChannelID int64,
	keywords []string,
) (Rule, error) {
	return s.CreateRuleWithOptions(
		ctx,
		name,
		accountID,
		peerType,
		peerID,
		peerTitle,
		notificationChannelID,
		keywords,
		0,
	)
}

func (s *Service) CreateRuleWithOptions(
	ctx context.Context,
	name string,
	accountID int64,
	peerType string,
	peerID int64,
	peerTitle string,
	notificationChannelID int64,
	keywords []string,
	cooldownSeconds int,
) (Rule, error) {
	return s.CreateRuleWithInput(ctx, RuleCreateInput{
		Name:                  name,
		AccountID:             accountID,
		PeerType:              peerType,
		PeerID:                peerID,
		PeerTitle:             peerTitle,
		NotificationChannelID: notificationChannelID,
		Keywords:              keywords,
		CooldownSeconds:       cooldownSeconds,
		Actor:                 "system",
	})
}

func (s *Service) CreateRuleWithInput(
	ctx context.Context,
	input RuleCreateInput,
) (Rule, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.PeerTitle = strings.TrimSpace(input.PeerTitle)
	input.PeerType = strings.ToLower(strings.TrimSpace(input.PeerType))
	input.Owner = strings.TrimSpace(input.Owner)
	input.Actor = strings.TrimSpace(input.Actor)
	if input.Actor == "" {
		input.Actor = "system"
	}
	if input.Owner == "" {
		input.Owner = input.Actor
	}
	if utf8.RuneCountInString(input.Actor) > 64 || utf8.RuneCountInString(input.Owner) > 64 {
		return Rule{}, errors.New("rule owner and actor must contain at most 64 characters")
	}
	if utf8.RuneCountInString(input.Name) < 1 || utf8.RuneCountInString(input.Name) > 60 {
		return Rule{}, errors.New("rule name must contain 1 to 60 characters")
	}
	if input.AccountID <= 0 || input.PeerID <= 0 {
		return Rule{}, errors.New("account ID and chat ID must be positive")
	}
	if input.PeerType != "user" && input.PeerType != "chat" && input.PeerType != "channel" {
		return Rule{}, errors.New("peer type must be user, chat, or channel")
	}
	if utf8.RuneCountInString(input.PeerTitle) > 120 {
		return Rule{}, errors.New("peer title must contain at most 120 characters")
	}
	if input.CooldownSeconds < 0 || input.CooldownSeconds > 86400 {
		return Rule{}, errors.New("rule cooldown must be between 0 and 86400 seconds")
	}
	var err error
	input.Keywords, err = normalizeRuleTerms(input.Keywords, "keyword")
	if err != nil {
		return Rule{}, err
	}
	input.ExcludeKeywords, err = normalizeRuleTerms(input.ExcludeKeywords, "exclude keyword")
	if err != nil {
		return Rule{}, err
	}
	if err := validateRuleTermsDoNotOverlap(input.Keywords, input.ExcludeKeywords); err != nil {
		return Rule{}, err
	}
	if input.NotificationChannelID <= 0 {
		input.NotificationChannelID = 1
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Rule{}, fmt.Errorf("begin create monitor rule: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateRuleChannel(ctx, tx, input.NotificationChannelID); err != nil {
		return Rule{}, err
	}
	keywords, err := json.Marshal(input.Keywords)
	if err != nil {
		return Rule{}, fmt.Errorf("encode monitor keywords: %w", err)
	}
	excludes, err := json.Marshal(input.ExcludeKeywords)
	if err != nil {
		return Rule{}, fmt.Errorf("encode monitor exclude keywords: %w", err)
	}
	now := s.now().UTC()
	timestamp := now.Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `
INSERT INTO monitor_rules (
	name, account_id, peer_type, peer_id, peer_title, keywords_json, exclude_keywords_json,
	cooldown_seconds, notification_channel_id, owner, version, created_by,
	enabled, last_message_id, last_run_at, last_error,
	created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, 1, 0, '', '', ?, ?)`,
		input.Name,
		input.AccountID,
		input.PeerType,
		input.PeerID,
		input.PeerTitle,
		string(keywords),
		string(excludes),
		input.CooldownSeconds,
		input.NotificationChannelID,
		input.Owner,
		input.Actor,
		timestamp,
		timestamp,
	)
	if err != nil {
		return Rule{}, fmt.Errorf("create monitor rule: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Rule{}, fmt.Errorf("read monitor rule ID: %w", err)
	}
	if err := insertRuleVersionTx(ctx, tx, Rule{
		ID:                    id,
		Name:                  input.Name,
		AccountID:             input.AccountID,
		PeerType:              input.PeerType,
		PeerID:                input.PeerID,
		PeerTitle:             input.PeerTitle,
		Keywords:              input.Keywords,
		ExcludeKeywords:       input.ExcludeKeywords,
		CooldownSeconds:       input.CooldownSeconds,
		NotificationChannelID: input.NotificationChannelID,
		Owner:                 input.Owner,
		Version:               1,
		Enabled:               true,
	}, input.Actor, now); err != nil {
		return Rule{}, err
	}
	if err := tx.Commit(); err != nil {
		return Rule{}, fmt.Errorf("commit monitor rule: %w", err)
	}
	return s.GetRule(ctx, id)
}

func (s *Service) ListNotificationChannels(ctx context.Context) ([]NotificationChannel, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, name, kind, enabled, config_json, created_at, updated_at
FROM notification_channels
ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list notification channels: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := make([]NotificationChannel, 0)
	for rows.Next() {
		var (
			item                 NotificationChannel
			enabled              int
			configJSON           string
			createdAt, updatedAt string
		)
		if err := rows.Scan(
			&item.ID,
			&item.Name,
			&item.Kind,
			&enabled,
			&configJSON,
			&createdAt,
			&updatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan notification channel: %w", err)
		}
		item.Enabled = enabled != 0
		item.Configured = item.Kind == "in_app" || notificationConfigConfigured(configJSON)
		item.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse notification channel created_at: %w", err)
		}
		item.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
		if err != nil {
			return nil, fmt.Errorf("parse notification channel updated_at: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate notification channels: %w", err)
	}
	return items, nil
}

func (s *Service) GetRule(ctx context.Context, id int64) (Rule, error) {
	rule, err := scanRule(s.db.QueryRowContext(ctx, `
SELECT r.id, r.name, r.account_id, r.peer_type, r.peer_id, r.peer_title,
	r.keywords_json, r.exclude_keywords_json, r.owner, r.version, r.created_by,
	r.cooldown_seconds, r.notification_channel_id,
	COALESCE(nc.name, ''), COALESCE(nc.kind, ''),
	r.enabled, r.last_message_id, r.last_run_at, r.last_error,
	r.created_at, r.updated_at
FROM monitor_rules r
LEFT JOIN notification_channels nc ON nc.id = r.notification_channel_id
WHERE r.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Rule{}, ErrNotFound
	}
	return rule, err
}

func (s *Service) ListRules(ctx context.Context, enabledOnly bool) ([]Rule, error) {
	query := `
SELECT r.id, r.name, r.account_id, r.peer_type, r.peer_id, r.peer_title,
	r.keywords_json, r.exclude_keywords_json, r.owner, r.version, r.created_by,
	r.cooldown_seconds, r.notification_channel_id,
	COALESCE(nc.name, ''), COALESCE(nc.kind, ''),
	r.enabled, r.last_message_id, r.last_run_at, r.last_error,
	r.created_at, r.updated_at
FROM monitor_rules r
LEFT JOIN notification_channels nc ON nc.id = r.notification_channel_id`
	if enabledOnly {
		query += " WHERE r.enabled = 1"
	}
	query += " ORDER BY r.created_at DESC, r.id DESC"
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list monitor rules: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := make([]Rule, 0)
	for rows.Next() {
		item, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate monitor rules: %w", err)
	}
	return items, nil
}

func (s *Service) SetRuleEnabled(
	ctx context.Context,
	id int64,
	enabled bool,
	actor string,
) error {
	_, err := s.UpdateRule(ctx, id, RuleUpdateInput{
		Enabled: &enabled,
		Actor:   actor,
	})
	return err
}

func (s *Service) UpdateRule(
	ctx context.Context,
	id int64,
	input RuleUpdateInput,
) (Rule, error) {
	if id <= 0 {
		return Rule{}, errors.New("rule ID must be positive")
	}
	input.Actor = strings.TrimSpace(input.Actor)
	if input.Actor == "" {
		input.Actor = "system"
	}
	if utf8.RuneCountInString(input.Actor) > 64 {
		return Rule{}, errors.New("rule actor must contain at most 64 characters")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Rule{}, fmt.Errorf("begin update monitor rule: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	current, err := scanRule(tx.QueryRowContext(ctx, `
SELECT r.id, r.name, r.account_id, r.peer_type, r.peer_id, r.peer_title,
	r.keywords_json, r.exclude_keywords_json, r.owner, r.version, r.created_by,
	r.cooldown_seconds, r.notification_channel_id,
	COALESCE(nc.name, ''), COALESCE(nc.kind, ''),
	r.enabled, r.last_message_id, r.last_run_at, r.last_error,
	r.created_at, r.updated_at
FROM monitor_rules r
LEFT JOIN notification_channels nc ON nc.id = r.notification_channel_id
WHERE r.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Rule{}, ErrNotFound
	}
	if err != nil {
		return Rule{}, fmt.Errorf("load monitor rule for update: %w", err)
	}
	if input.Name != nil {
		current.Name = strings.TrimSpace(*input.Name)
	}
	if input.AccountID != nil {
		current.AccountID = *input.AccountID
	}
	if input.PeerType != nil {
		current.PeerType = strings.ToLower(strings.TrimSpace(*input.PeerType))
	}
	if input.PeerID != nil {
		current.PeerID = *input.PeerID
	}
	if input.PeerTitle != nil {
		current.PeerTitle = strings.TrimSpace(*input.PeerTitle)
	}
	if input.NotificationChannelID != nil {
		current.NotificationChannelID = *input.NotificationChannelID
	}
	if input.Keywords != nil {
		current.Keywords = append([]string(nil), (*input.Keywords)...)
	}
	if input.ExcludeKeywords != nil {
		current.ExcludeKeywords = append([]string(nil), (*input.ExcludeKeywords)...)
	}
	if input.CooldownSeconds != nil {
		current.CooldownSeconds = *input.CooldownSeconds
	}
	if input.Owner != nil {
		current.Owner = strings.TrimSpace(*input.Owner)
	}
	if input.Enabled != nil {
		current.Enabled = *input.Enabled
	}
	if current.Name == "" || utf8.RuneCountInString(current.Name) > 60 {
		return Rule{}, errors.New("rule name must contain 1 to 60 characters")
	}
	if current.AccountID <= 0 || current.PeerID <= 0 {
		return Rule{}, errors.New("account ID and chat ID must be positive")
	}
	if current.PeerType != "user" && current.PeerType != "chat" && current.PeerType != "channel" {
		return Rule{}, errors.New("peer type must be user, chat, or channel")
	}
	if utf8.RuneCountInString(current.PeerTitle) > 120 {
		return Rule{}, errors.New("peer title must contain at most 120 characters")
	}
	if current.CooldownSeconds < 0 || current.CooldownSeconds > 86400 {
		return Rule{}, errors.New("rule cooldown must be between 0 and 86400 seconds")
	}
	if current.Owner == "" {
		current.Owner = input.Actor
	}
	if utf8.RuneCountInString(current.Owner) > 64 {
		return Rule{}, errors.New("rule owner must contain at most 64 characters")
	}
	current.Keywords, err = normalizeRuleTerms(current.Keywords, "keyword")
	if err != nil {
		return Rule{}, err
	}
	current.ExcludeKeywords, err = normalizeRuleTerms(current.ExcludeKeywords, "exclude keyword")
	if err != nil {
		return Rule{}, err
	}
	if err := validateRuleTermsDoNotOverlap(current.Keywords, current.ExcludeKeywords); err != nil {
		return Rule{}, err
	}
	if current.NotificationChannelID <= 0 {
		current.NotificationChannelID = 1
	}
	if err := validateRuleChannel(ctx, tx, current.NotificationChannelID); err != nil {
		return Rule{}, err
	}
	keywords, err := json.Marshal(current.Keywords)
	if err != nil {
		return Rule{}, fmt.Errorf("encode monitor keywords: %w", err)
	}
	excludes, err := json.Marshal(current.ExcludeKeywords)
	if err != nil {
		return Rule{}, fmt.Errorf("encode monitor exclude keywords: %w", err)
	}
	nextVersion := current.Version + 1
	now := s.now().UTC()
	result, err := tx.ExecContext(ctx, `
UPDATE monitor_rules
SET name = ?, account_id = ?, peer_type = ?, peer_id = ?, peer_title = ?,
	keywords_json = ?, exclude_keywords_json = ?,
	cooldown_seconds = ?, notification_channel_id = ?, owner = ?,
	version = ?, enabled = ?, updated_at = ?
WHERE id = ? AND version = ?`,
		current.Name,
		current.AccountID,
		current.PeerType,
		current.PeerID,
		current.PeerTitle,
		string(keywords),
		string(excludes),
		current.CooldownSeconds,
		current.NotificationChannelID,
		current.Owner,
		nextVersion,
		boolInt(current.Enabled),
		now.Format(time.RFC3339Nano),
		id,
		current.Version,
	)
	if err != nil {
		return Rule{}, fmt.Errorf("update monitor rule: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Rule{}, fmt.Errorf("check updated monitor rule: %w", err)
	}
	if affected == 0 {
		return Rule{}, errors.New("monitor rule was changed concurrently")
	}
	current.Version = nextVersion
	current.UpdatedAt = now
	if err := insertRuleVersionTx(ctx, tx, current, input.Actor, now); err != nil {
		return Rule{}, err
	}
	if err := tx.Commit(); err != nil {
		return Rule{}, fmt.Errorf("commit monitor rule update: %w", err)
	}
	return s.GetRule(ctx, id)
}

func (s *Service) ListRuleVersions(ctx context.Context, id int64) ([]RuleVersion, error) {
	if id <= 0 {
		return nil, errors.New("rule ID must be positive")
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT rule_id, version, name, account_id, peer_type, peer_id, peer_title,
	keywords_json, exclude_keywords_json, cooldown_seconds,
	notification_channel_id, owner, enabled, actor, created_at
FROM monitor_rule_versions
WHERE rule_id = ?
ORDER BY version DESC`, id)
	if err != nil {
		return nil, fmt.Errorf("list monitor rule versions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := make([]RuleVersion, 0)
	for rows.Next() {
		var (
			item                      RuleVersion
			keywordsJSON, excludeJSON string
			enabled                   int
			createdAt                 string
		)
		if err := rows.Scan(
			&item.RuleID,
			&item.Version,
			&item.Name,
			&item.AccountID,
			&item.PeerType,
			&item.PeerID,
			&item.PeerTitle,
			&keywordsJSON,
			&excludeJSON,
			&item.CooldownSeconds,
			&item.NotificationChannelID,
			&item.Owner,
			&enabled,
			&item.Actor,
			&createdAt,
		); err != nil {
			return nil, fmt.Errorf("scan monitor rule version: %w", err)
		}
		item.Enabled = enabled != 0
		if err := json.Unmarshal([]byte(keywordsJSON), &item.Keywords); err != nil {
			return nil, fmt.Errorf("decode monitor rule version keywords: %w", err)
		}
		if err := json.Unmarshal([]byte(excludeJSON), &item.ExcludeKeywords); err != nil {
			return nil, fmt.Errorf("decode monitor rule version exclude keywords: %w", err)
		}
		item.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse monitor rule version time: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate monitor rule versions: %w", err)
	}
	return items, nil
}

func (s *Service) PreviewRule(
	ctx context.Context,
	input RulePreviewInput,
) (RulePreview, error) {
	input.PeerType = strings.ToLower(strings.TrimSpace(input.PeerType))
	if input.AccountID <= 0 || input.PeerID <= 0 {
		return RulePreview{}, errors.New("account ID and chat ID must be positive")
	}
	if input.PeerType != "user" && input.PeerType != "chat" && input.PeerType != "channel" {
		return RulePreview{}, errors.New("peer type must be user, chat, or channel")
	}
	var err error
	input.Keywords, err = normalizeRuleTerms(input.Keywords, "keyword")
	if err != nil {
		return RulePreview{}, err
	}
	input.ExcludeKeywords, err = normalizeRuleTerms(input.ExcludeKeywords, "exclude keyword")
	if err != nil {
		return RulePreview{}, err
	}
	if err := validateRuleTermsDoNotOverlap(input.Keywords, input.ExcludeKeywords); err != nil {
		return RulePreview{}, err
	}
	input.Limit = normalizeLimit(input.Limit, 10, 20)
	rows, err := s.db.QueryContext(ctx, `
SELECT message_id, sender, text, sent_at
FROM indexed_messages
WHERE account_id = ? AND peer_type = ? AND peer_id = ? AND deleted_at = ''
ORDER BY message_id DESC
LIMIT 500`,
		input.AccountID,
		input.PeerType,
		input.PeerID,
	)
	if err != nil {
		return RulePreview{}, fmt.Errorf("preview monitor rule: %w", err)
	}
	defer func() { _ = rows.Close() }()
	preview := RulePreview{
		Samples:       make([]RulePreviewSample, 0, input.Limit),
		ExcludedItems: make([]RulePreviewSample, 0, input.Limit),
	}
	for rows.Next() {
		var (
			sample RulePreviewSample
			sentAt string
		)
		if err := rows.Scan(&sample.MessageID, &sample.Sender, &sample.Text, &sentAt); err != nil {
			return RulePreview{}, fmt.Errorf("scan monitor rule preview: %w", err)
		}
		sample.SentAt, err = time.Parse(time.RFC3339Nano, sentAt)
		if err != nil {
			return RulePreview{}, fmt.Errorf("parse monitor rule preview time: %w", err)
		}
		preview.Scanned++
		keyword := matchedKeyword(input.Keywords, sample.Text)
		if len(input.Keywords) > 0 && keyword == "" {
			continue
		}
		sample.MatchedKeyword = keyword
		if exclusion := matchedKeyword(input.ExcludeKeywords, sample.Text); exclusion != "" {
			preview.Excluded++
			sample.ExcludedBy = exclusion
			sample.Text = truncate(strings.TrimSpace(sample.Text), 280)
			if len(preview.ExcludedItems) < input.Limit {
				preview.ExcludedItems = append(preview.ExcludedItems, sample)
			}
			continue
		}
		preview.Matched++
		sample.Text = truncate(strings.TrimSpace(sample.Text), 280)
		if len(preview.Samples) < input.Limit {
			preview.Samples = append(preview.Samples, sample)
		}
	}
	if err := rows.Err(); err != nil {
		return RulePreview{}, fmt.Errorf("iterate monitor rule preview: %w", err)
	}
	return preview, nil
}

func (s *Service) DeleteRule(ctx context.Context, id int64) error {
	if id <= 0 {
		return errors.New("rule ID must be positive")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM monitor_rules WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete monitor rule: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check deleted monitor rule: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Service) MarkRuleRun(ctx context.Context, id int64, lastMessageID int, runErr error) error {
	errorText := ""
	if runErr != nil {
		errorText = truncate(runErr.Error(), 180)
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE monitor_rules
SET last_message_id = CASE WHEN ? > last_message_id THEN ? ELSE last_message_id END,
	last_run_at = ?, last_error = ?, updated_at = ?
WHERE id = ?`,
		lastMessageID,
		lastMessageID,
		s.now().UTC().Format(time.RFC3339Nano),
		errorText,
		s.now().UTC().Format(time.RFC3339Nano),
		id,
	)
	if err != nil {
		return fmt.Errorf("record monitor run: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check monitor run: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

type MessageRecord struct {
	AccountID int64
	PeerType  string
	PeerID    int64
	PeerTitle string
	MessageID int
	Sender    string
	Text      string
	MediaType string
	Media     MediaRecord
	SentAt    time.Time
}

type MediaRecord struct {
	Type          string `json:"type,omitempty"`
	Kind          string `json:"kind,omitempty"`
	ID            int64  `json:"id,omitempty"`
	DCID          int    `json:"dc_id,omitempty"`
	MimeType      string `json:"mime_type,omitempty"`
	FileName      string `json:"file_name,omitempty"`
	Size          int64  `json:"size,omitempty"`
	Width         int    `json:"width,omitempty"`
	Height        int    `json:"height,omitempty"`
	Spoiler       bool   `json:"spoiler,omitempty"`
	Video         bool   `json:"video,omitempty"`
	Voice         bool   `json:"voice,omitempty"`
	Round         bool   `json:"round,omitempty"`
	AccessHash    int64  `json:"access_hash,omitempty"`
	FileReference string `json:"file_reference,omitempty"`
}

func (r Rule) AppliesTo(message MessageRecord) bool {
	return r.AccountID == message.AccountID &&
		r.PeerType == message.PeerType &&
		r.PeerID == message.PeerID
}

func (s *Service) insertAlert(
	ctx context.Context,
	tx *sql.Tx,
	rule Rule,
	message MessageRecord,
	keyword string,
	now time.Time,
) (Alert, error) {
	title := message.PeerTitle
	if title == "" {
		title = rule.PeerTitle
	}
	preview := truncate(strings.TrimSpace(message.Text), 280)
	if rule.CooldownSeconds > 0 {
		var lastAlertAt string
		err := tx.QueryRowContext(ctx, `
SELECT created_at FROM monitor_alerts
WHERE rule_id = ?
ORDER BY created_at DESC, id DESC
LIMIT 1`, rule.ID).Scan(&lastAlertAt)
		if err == nil {
			lastAlert, parseErr := time.Parse(time.RFC3339Nano, lastAlertAt)
			if parseErr != nil {
				return Alert{}, fmt.Errorf("parse previous alert time: %w", parseErr)
			}
			if now.Before(lastAlert.Add(time.Duration(rule.CooldownSeconds) * time.Second)) {
				return Alert{}, nil
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return Alert{}, fmt.Errorf("read previous alert time: %w", err)
		}
	}
	result, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO monitor_alerts (
	rule_id, rule_version, account_id, peer_type, peer_id, peer_title, message_id,
	matched_keyword, message_preview, state, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?)`,
		rule.ID,
		rule.Version,
		rule.AccountID,
		rule.PeerType,
		rule.PeerID,
		truncate(title, 120),
		message.MessageID,
		truncate(keyword, 80),
		preview,
		now.Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano),
	)
	if err != nil {
		return Alert{}, fmt.Errorf("create monitor alert: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Alert{}, fmt.Errorf("read monitor alert ID: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Alert{}, fmt.Errorf("check monitor alert: %w", err)
	}
	if affected == 0 {
		return Alert{}, nil
	}
	if rule.NotificationChannelKind != "in_app" && rule.NotificationChannelKind != "webhook" {
		return Alert{}, errors.New("notification channel kind is not supported")
	}
	alert := Alert{
		ID:             id,
		RuleID:         rule.ID,
		RuleName:       rule.Name,
		RuleVersion:    rule.Version,
		AccountID:      rule.AccountID,
		PeerType:       rule.PeerType,
		PeerID:         rule.PeerID,
		PeerTitle:      title,
		MessageID:      message.MessageID,
		MatchedKeyword: keyword,
		MessagePreview: preview,
		State:          "pending",
		ChannelID:      rule.NotificationChannelID,
		ChannelName:    rule.NotificationChannelName,
		Severity:       "medium",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	deliveryState := "queued"
	attemptCount := 0
	lastAttemptAt := ""
	deliveredAt := ""
	if rule.NotificationChannelKind == "in_app" {
		deliveryState = "delivered"
		attemptCount = 1
		lastAttemptAt = now.Format(time.RFC3339Nano)
		deliveredAt = lastAttemptAt
	}
	delivery, err := tx.ExecContext(ctx, `
INSERT INTO notification_deliveries (
	alert_id, channel_id, channel_kind, state, attempt_count,
	last_attempt_at, delivered_at, next_attempt_at, error_text,
	failure_alerted_at, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, '', '', '', ?, ?)`,
		alert.ID,
		rule.NotificationChannelID,
		rule.NotificationChannelKind,
		deliveryState,
		attemptCount,
		lastAttemptAt,
		deliveredAt,
		now.Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano),
	)
	if err != nil {
		return Alert{}, fmt.Errorf("record notification delivery: %w", err)
	}
	deliveryID, err := delivery.LastInsertId()
	if err != nil {
		return Alert{}, fmt.Errorf("read notification delivery ID: %w", err)
	}
	alert.DeliveryID = deliveryID
	alert.DeliveryState = deliveryState
	alert.DeliveryCount = attemptCount
	if lastAttemptAt != "" {
		alert.DeliveryLastAt = &now
	}
	if deliveredAt != "" {
		alert.DeliveryAt = &now
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO monitor_alert_events (
	alert_id, action, actor, note, previous_state, next_state, created_at
) VALUES (?, 'created', 'system', '', '', 'pending', ?)`,
		alert.ID,
		now.Format(time.RFC3339Nano),
	); err != nil {
		return Alert{}, fmt.Errorf("record monitor alert creation: %w", err)
	}
	return alert, nil
}

const selectAlertQuery = `
SELECT a.id, a.rule_id,
	COALESCE(
		(SELECT rv.name FROM monitor_rule_versions rv
		 WHERE rv.rule_id = a.rule_id AND rv.version = a.rule_version),
		COALESCE(r.name, '')
	),
	a.account_id, a.peer_type, a.peer_id,
	a.rule_version, a.peer_title, a.message_id, a.matched_keyword, a.message_preview, a.state,
	COALESCE(r.notification_channel_id, 0), COALESCE(nc.name, ''), a.created_at,
	COALESCE(d.id, 0), COALESCE(d.state, ''), COALESCE(d.attempt_count, 0),
	COALESCE(d.last_attempt_at, ''), COALESCE(d.delivered_at, ''),
	COALESCE(d.next_attempt_at, ''), COALESCE(d.error_text, ''), a.severity, a.assigned_to,
	a.acknowledged_by, a.acknowledged_at, a.resolved_by, a.resolved_at,
	a.updated_at
FROM monitor_alerts a
LEFT JOIN monitor_rules r ON r.id = a.rule_id
LEFT JOIN notification_channels nc ON nc.id = r.notification_channel_id
LEFT JOIN notification_deliveries d ON d.alert_id = a.id`

func (s *Service) ListAlerts(ctx context.Context, state string, limit int) ([]Alert, error) {
	if state != "" && state != "pending" && state != "resolved" {
		if state != "acknowledged" {
			return nil, errors.New("alert state must be pending, acknowledged, or resolved")
		}
	}
	limit = normalizeLimit(limit, 50, 200)
	query := selectAlertQuery
	args := make([]any, 0, 2)
	if state != "" {
		query += " WHERE a.state = ?"
		args = append(args, state)
	}
	query += " ORDER BY a.created_at DESC, a.id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list monitor alerts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := make([]Alert, 0)
	for rows.Next() {
		item, err := scanAlert(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate monitor alerts: %w", err)
	}
	return items, nil
}

func (s *Service) GetAlert(ctx context.Context, id int64) (Alert, error) {
	if id <= 0 {
		return Alert{}, ErrNotFound
	}
	item, err := scanAlert(s.db.QueryRowContext(
		ctx,
		selectAlertQuery+" WHERE a.id = ?",
		id,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return Alert{}, ErrNotFound
	}
	return item, err
}

func (s *Service) ListAlertEvents(
	ctx context.Context,
	alertID int64,
	limit int,
) ([]AlertEvent, error) {
	if alertID <= 0 {
		return nil, ErrNotFound
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `
SELECT 1 FROM monitor_alerts WHERE id = ?`, alertID,
	).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, fmt.Errorf("check monitor alert: %w", err)
	}
	limit = normalizeLimit(limit, 100, 200)
	rows, err := s.db.QueryContext(ctx, `
SELECT sequence, alert_id, action, actor, note,
	previous_state, next_state, created_at
FROM monitor_alert_events
WHERE alert_id = ?
ORDER BY sequence DESC
LIMIT ?`,
		alertID,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list monitor alert events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := make([]AlertEvent, 0)
	for rows.Next() {
		var (
			item      AlertEvent
			createdAt string
		)
		if err := rows.Scan(
			&item.Sequence,
			&item.AlertID,
			&item.Action,
			&item.Actor,
			&item.Note,
			&item.PreviousState,
			&item.NextState,
			&createdAt,
		); err != nil {
			return nil, fmt.Errorf("scan monitor alert event: %w", err)
		}
		item.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse monitor alert event time: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate monitor alert events: %w", err)
	}
	return items, nil
}

func (s *Service) UpdateAlert(
	ctx context.Context,
	id int64,
	input AlertUpdateInput,
) (Alert, error) {
	if id <= 0 {
		return Alert{}, ErrNotFound
	}
	normalized, err := normalizeAlertUpdateInput(input)
	if err != nil {
		return Alert{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Alert{}, fmt.Errorf("begin monitor alert update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var currentState string
	err = tx.QueryRowContext(ctx,
		`SELECT state FROM monitor_alerts WHERE id = ?`,
		id,
	).Scan(&currentState)
	if errors.Is(err, sql.ErrNoRows) {
		return Alert{}, ErrNotFound
	}
	if err != nil {
		return Alert{}, fmt.Errorf("read monitor alert state: %w", err)
	}
	nextState, err := validateAlertTransition(currentState, normalized)
	if err != nil {
		return Alert{}, err
	}
	now := s.now().UTC()
	if err := applyAlertUpdate(ctx, tx, id, normalized, now); err != nil {
		return Alert{}, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO monitor_alert_events (
	alert_id, action, actor, note, previous_state, next_state, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id,
		alertEventAction(normalized.Action),
		normalized.Actor,
		alertEventNote(normalized),
		currentState,
		nextState,
		now.Format(time.RFC3339Nano),
	); err != nil {
		return Alert{}, fmt.Errorf("record monitor alert event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Alert{}, fmt.Errorf("commit monitor alert update: %w", err)
	}
	return s.GetAlert(ctx, id)
}

func normalizeAlertUpdateInput(input AlertUpdateInput) (AlertUpdateInput, error) {
	input.Action = strings.ToLower(strings.TrimSpace(input.Action))
	input.Severity = strings.ToLower(strings.TrimSpace(input.Severity))
	input.Assignee = strings.TrimSpace(input.Assignee)
	input.Note = strings.TrimSpace(input.Note)
	input.Actor = strings.TrimSpace(input.Actor)
	if input.Actor == "" || utf8.RuneCountInString(input.Actor) > 64 {
		return AlertUpdateInput{}, fmt.Errorf("%w: actor is required", ErrAlertInvalid)
	}
	if utf8.RuneCountInString(input.Note) > 500 {
		return AlertUpdateInput{}, fmt.Errorf("%w: note must contain at most 500 characters", ErrAlertInvalid)
	}
	if utf8.RuneCountInString(input.Assignee) > 64 {
		return AlertUpdateInput{}, fmt.Errorf("%w: assignee must contain at most 64 characters", ErrAlertInvalid)
	}
	switch input.Action {
	case "acknowledge", "resolve", "reopen":
	case "assign":
		if input.Assignee == "" {
			return AlertUpdateInput{}, fmt.Errorf("%w: assignee is required", ErrAlertInvalid)
		}
	case "set_severity":
		switch input.Severity {
		case "low", "medium", "high", "critical":
		default:
			return AlertUpdateInput{}, fmt.Errorf("%w: severity must be low, medium, high, or critical", ErrAlertInvalid)
		}
	case "note":
		if input.Note == "" {
			return AlertUpdateInput{}, fmt.Errorf("%w: note is required", ErrAlertInvalid)
		}
	default:
		return AlertUpdateInput{}, fmt.Errorf("%w: unsupported action", ErrAlertInvalid)
	}
	return input, nil
}

func validateAlertTransition(state string, input AlertUpdateInput) (string, error) {
	switch input.Action {
	case "acknowledge":
		if state != "pending" {
			return "", fmt.Errorf("%w: only pending alerts can be acknowledged", ErrAlertConflict)
		}
		return "acknowledged", nil
	case "resolve":
		if state != "pending" && state != "acknowledged" {
			return "", fmt.Errorf("%w: only open alerts can be resolved", ErrAlertConflict)
		}
		return "resolved", nil
	case "reopen":
		if state != "resolved" {
			return "", fmt.Errorf("%w: only resolved alerts can be reopened", ErrAlertConflict)
		}
		return "pending", nil
	case "assign", "set_severity":
		if state == "resolved" {
			return "", fmt.Errorf("%w: resolved alerts must be reopened before this action", ErrAlertConflict)
		}
	}
	return state, nil
}

func applyAlertUpdate(
	ctx context.Context,
	tx *sql.Tx,
	id int64,
	input AlertUpdateInput,
	now time.Time,
) error {
	timestamp := now.Format(time.RFC3339Nano)
	var (
		query string
		args  []any
	)
	switch input.Action {
	case "acknowledge":
		query = `UPDATE monitor_alerts
SET state = 'acknowledged', acknowledged_by = ?, acknowledged_at = ?, updated_at = ?
WHERE id = ?`
		args = []any{input.Actor, timestamp, timestamp, id}
	case "resolve":
		query = `UPDATE monitor_alerts
SET state = 'resolved', resolved_by = ?, resolved_at = ?, updated_at = ?
WHERE id = ?`
		args = []any{input.Actor, timestamp, timestamp, id}
	case "reopen":
		query = `UPDATE monitor_alerts
SET state = 'pending', acknowledged_by = '', acknowledged_at = '',
	resolved_by = '', resolved_at = '', updated_at = ?
WHERE id = ?`
		args = []any{timestamp, id}
	case "assign":
		query = `UPDATE monitor_alerts SET assigned_to = ?, updated_at = ? WHERE id = ?`
		args = []any{input.Assignee, timestamp, id}
	case "set_severity":
		query = `UPDATE monitor_alerts SET severity = ?, updated_at = ? WHERE id = ?`
		args = []any{input.Severity, timestamp, id}
	case "note":
		query = `UPDATE monitor_alerts SET updated_at = ? WHERE id = ?`
		args = []any{timestamp, id}
	default:
		return fmt.Errorf("%w: unsupported action", ErrAlertInvalid)
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("apply monitor alert update: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check monitor alert update: %w", err)
	}
	if affected != 1 {
		return ErrNotFound
	}
	return nil
}

func alertEventAction(action string) string {
	switch action {
	case "acknowledge":
		return "acknowledged"
	case "assign":
		return "assigned"
	case "set_severity":
		return "severity_changed"
	case "note":
		return "noted"
	case "resolve":
		return "resolved"
	case "reopen":
		return "reopened"
	}
	return action
}

func alertEventNote(input AlertUpdateInput) string {
	detail := input.Note
	var change string
	switch input.Action {
	case "assign":
		change = "assignee=" + input.Assignee
	case "set_severity":
		change = "severity=" + input.Severity
	}
	if change == "" {
		return detail
	}
	if detail == "" {
		return change
	}
	return change + "; " + detail
}

func (s *Service) SearchMessages(ctx context.Context, filter MessageQuery) (MessagePage, error) {
	filter.Query = strings.TrimSpace(filter.Query)
	if utf8.RuneCountInString(filter.Query) > 100 {
		return MessagePage{}, errors.New("search query must contain at most 100 characters")
	}
	if filter.PeerType != "" && filter.PeerType != "user" &&
		filter.PeerType != "chat" && filter.PeerType != "channel" {
		return MessagePage{}, errors.New("peer type must be user, chat, or channel")
	}
	filter.Limit = normalizeLimit(filter.Limit, 50, 100)
	if filter.Offset < 0 {
		return MessagePage{}, errors.New("offset must not be negative")
	}
	where := []string{"deleted_at = ''"}
	args := make([]any, 0, 6)
	if filter.Query != "" {
		if utf8.RuneCountInString(filter.Query) >= 3 {
			where = append(where, `rowid IN (
	SELECT rowid FROM indexed_messages_fts
	WHERE indexed_messages_fts MATCH ?
)`)
			args = append(args, ftsPhrase(filter.Query))
		} else {
			where = append(where, "(text LIKE ? ESCAPE '!' OR sender LIKE ? ESCAPE '!' OR peer_title LIKE ? ESCAPE '!')")
			pattern := "%" + escapeLike(filter.Query) + "%"
			args = append(args, pattern, pattern, pattern)
		}
	}
	if filter.AccountID > 0 {
		where = append(where, "account_id = ?")
		args = append(args, filter.AccountID)
	}
	if filter.PeerType != "" {
		where = append(where, "peer_type = ?")
		args = append(args, filter.PeerType)
	}
	if !filter.From.IsZero() {
		where = append(where, "sent_at >= ?")
		args = append(args, filter.From.UTC().Format(time.RFC3339Nano))
	}
	if !filter.To.IsZero() {
		where = append(where, "sent_at < ?")
		args = append(args, filter.To.UTC().Format(time.RFC3339Nano))
	}
	condition := ""
	if len(where) > 0 {
		condition = " WHERE " + strings.Join(where, " AND ")
	}
	var total int
	if err := s.db.QueryRowContext(
		ctx,
		"SELECT COUNT(1) FROM indexed_messages"+condition,
		args...,
	).Scan(&total); err != nil {
		return MessagePage{}, fmt.Errorf("count indexed messages: %w", err)
	}
	queryArgs := append(append([]any(nil), args...), filter.Limit, filter.Offset)
	rows, err := s.db.QueryContext(ctx, `
SELECT account_id, peer_type, peer_id, peer_title, message_id, sender, text, sent_at, indexed_at
FROM indexed_messages`+condition+`
ORDER BY sent_at DESC, message_id DESC
LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return MessagePage{}, fmt.Errorf("search indexed messages: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := make([]IndexedMessage, 0, filter.Limit)
	for rows.Next() {
		item, err := scanIndexedMessage(rows)
		if err != nil {
			return MessagePage{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return MessagePage{}, fmt.Errorf("iterate indexed messages: %w", err)
	}
	return MessagePage{
		Items:  items,
		Total:  total,
		Limit:  filter.Limit,
		Offset: filter.Offset,
	}, nil
}

func (s *Service) BuildReport(ctx context.Context, days int) (Report, error) {
	if days < 1 {
		days = 7
	}
	if days > 90 {
		days = 90
	}
	since := s.now().UTC().AddDate(0, 0, -days+1).Truncate(24 * time.Hour)
	rows, err := s.db.QueryContext(ctx, `
SELECT substr(sent_at, 1, 10) AS day, COUNT(1)
FROM indexed_messages
WHERE sent_at >= ? AND deleted_at = ''
GROUP BY day
ORDER BY day`, since.Format(time.RFC3339Nano))
	if err != nil {
		return Report{}, fmt.Errorf("build daily message report: %w", err)
	}
	defer func() { _ = rows.Close() }()
	byDay := make([]DailyCount, 0, days)
	total := 0
	for rows.Next() {
		var item DailyCount
		if err := rows.Scan(&item.Date, &item.Count); err != nil {
			return Report{}, fmt.Errorf("scan daily message report: %w", err)
		}
		byDay = append(byDay, item)
		total += item.Count
	}
	if err := rows.Err(); err != nil {
		return Report{}, fmt.Errorf("iterate daily message report: %w", err)
	}
	accountRows, err := s.db.QueryContext(ctx, `
SELECT account_id, COUNT(1)
FROM indexed_messages
WHERE sent_at >= ? AND deleted_at = ''
GROUP BY account_id
ORDER BY COUNT(1) DESC`, since.Format(time.RFC3339Nano))
	if err != nil {
		return Report{}, fmt.Errorf("build account message report: %w", err)
	}
	defer func() { _ = accountRows.Close() }()
	byAccount := make([]AccountCount, 0)
	for accountRows.Next() {
		var item AccountCount
		if err := accountRows.Scan(&item.AccountID, &item.Count); err != nil {
			return Report{}, fmt.Errorf("scan account message report: %w", err)
		}
		byAccount = append(byAccount, item)
	}
	if err := accountRows.Err(); err != nil {
		return Report{}, fmt.Errorf("iterate account message report: %w", err)
	}
	return Report{
		Days:        days,
		Total:       total,
		ByDay:       byDay,
		ByAccount:   byAccount,
		GeneratedAt: s.now().UTC(),
	}, nil
}

func (s *Service) Dashboard(ctx context.Context, telegramReady bool) (Dashboard, error) {
	var result Dashboard
	result.TelegramReady = telegramReady
	now := s.now().UTC()
	queries := []struct {
		dest  *int
		query string
		args  []any
	}{
		{
			dest:  &result.AccountCount,
			query: `SELECT COUNT(1) FROM accounts WHERE purge_state = 'active'`,
		},
		{
			dest: &result.OnlineCount,
			query: `SELECT COUNT(1) FROM accounts
WHERE status = 'online' AND purge_state = 'active' AND last_verified_at >= ?`,
			args: []any{now.Add(-10 * time.Minute).Format(time.RFC3339Nano)},
		},
		{dest: &result.MonitorCount, query: `SELECT COUNT(1) FROM monitor_rules WHERE enabled = 1`},
		{
			dest: &result.TodayMessages,
			query: `SELECT COUNT(1) FROM indexed_messages
WHERE sent_at >= ? AND deleted_at = ''`,
			args: []any{now.Truncate(24 * time.Hour).Format(time.RFC3339Nano)},
		},
		{
			dest: &result.PendingAlerts,
			query: `SELECT COUNT(1) FROM monitor_alerts
WHERE state IN ('pending', 'acknowledged')`,
		},
	}
	for _, item := range queries {
		if err := s.db.QueryRowContext(ctx, item.query, item.args...).Scan(item.dest); err != nil {
			return Dashboard{}, fmt.Errorf("build dashboard statistics: %w", err)
		}
	}
	return result, nil
}

func (s *Service) PurgeAccount(ctx context.Context, accountID int64) error {
	if err := s.CheckAccountPurge(ctx, accountID); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin purge account operations: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var heldAccountID int64
	if err := tx.QueryRowContext(ctx, `
SELECT account_id
FROM legal_holds
WHERE account_id = ? AND released_at = ''
LIMIT 1`, accountID).Scan(&heldAccountID); err == nil {
		return ErrActiveLegalHold
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check account legal holds: %w", err)
	}
	summary, err := summarizeAccountDestructionTx(ctx, tx, accountID)
	if err != nil {
		return err
	}
	for _, query := range []string{
		`DELETE FROM account_group_members WHERE account_id = ?`,
		`DELETE FROM monitor_alert_events
			WHERE alert_id IN (SELECT id FROM monitor_alerts WHERE account_id = ?)`,
		`DELETE FROM notification_deliveries
			WHERE alert_id IN (SELECT id FROM monitor_alerts WHERE account_id = ?)`,
		`DELETE FROM monitor_alerts WHERE account_id = ?`,
		`DELETE FROM monitor_rules WHERE account_id = ?`,
		`DELETE FROM indexed_messages WHERE account_id = ?`,
		`DELETE FROM monitor_checkpoints WHERE account_id = ?`,
		`DELETE FROM monitor_account_states WHERE account_id = ?`,
		`DELETE FROM ingestion_batches WHERE account_id = ?`,
		`DELETE FROM evidence_events WHERE account_id = ?`,
		`DELETE FROM pending_message_deletions WHERE account_id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, query, accountID); err != nil {
			return fmt.Errorf("purge account operations: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO account_destructions (
	account_id, destroyed_at, reason, evidence_event_count,
	ingestion_batch_count, content_digest, batch_digest, schema_version
) VALUES (?, ?, 'account_purge', ?, ?, ?, ?, 1)`,
		accountID,
		s.now().UTC().Format(time.RFC3339Nano),
		summary.EvidenceEventCount,
		summary.IngestionBatchCount,
		summary.ContentDigest,
		summary.BatchDigest,
	); err != nil {
		return fmt.Errorf("record account destruction: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit purge account operations: %w", err)
	}
	return nil
}

type accountDestructionSummary struct {
	EvidenceEventCount  int
	IngestionBatchCount int
	ContentDigest       string
	BatchDigest         string
}

func summarizeAccountDestructionTx(
	ctx context.Context,
	tx *sql.Tx,
	accountID int64,
) (accountDestructionSummary, error) {
	var summary accountDestructionSummary
	contentDigest := sha256.New()
	_, _ = io.WriteString(contentDigest, "telenexus-account-destruction-events-v1\n")
	rows, err := tx.QueryContext(ctx, `
SELECT sequence, event_id, batch_id, peer_type, peer_id, message_id, event_type,
	version, observed_at, content_sha256, schema_version
FROM evidence_events
WHERE account_id = ?
ORDER BY sequence`, accountID)
	if err != nil {
		return summary, fmt.Errorf("read account destruction events: %w", err)
	}
	for rows.Next() {
		var (
			sequence, peerID, messageID int64
			eventID, batchID, peerType  string
			eventType, observedAt       string
			contentSHA256               string
			version, schemaVersion      int
		)
		if err := rows.Scan(
			&sequence,
			&eventID,
			&batchID,
			&peerType,
			&peerID,
			&messageID,
			&eventType,
			&version,
			&observedAt,
			&contentSHA256,
			&schemaVersion,
		); err != nil {
			_ = rows.Close()
			return summary, fmt.Errorf("scan account destruction event: %w", err)
		}
		if _, err := fmt.Fprintf(
			contentDigest,
			"%d\x00%s\x00%s\x00%s\x00%d\x00%d\x00%s\x00%d\x00%s\x00%s\x00%d\n",
			sequence,
			eventID,
			batchID,
			peerType,
			peerID,
			messageID,
			eventType,
			version,
			observedAt,
			contentSHA256,
			schemaVersion,
		); err != nil {
			_ = rows.Close()
			return summary, fmt.Errorf("hash account destruction event: %w", err)
		}
		summary.EvidenceEventCount++
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return summary, fmt.Errorf("iterate account destruction events: %w", err)
	}
	if err := rows.Close(); err != nil {
		return summary, fmt.Errorf("close account destruction events: %w", err)
	}
	summary.ContentDigest = hex.EncodeToString(contentDigest.Sum(nil))

	batchDigest := sha256.New()
	_, _ = io.WriteString(batchDigest, "telenexus-account-destruction-batches-v1\n")
	rows, err = tx.QueryContext(ctx, `
SELECT batch_id, source, peer_type, peer_id, status, started_at, finished_at,
	fetched_count, event_count, inserted_count, checkpoint_before,
	checkpoint_after, backlog_detected, payload_sha256, error_text
FROM ingestion_batches
WHERE account_id = ?
ORDER BY started_at, batch_id`, accountID)
	if err != nil {
		return summary, fmt.Errorf("read account destruction batches: %w", err)
	}
	for rows.Next() {
		var (
			batchID, source, peerType, status string
			startedAt, finishedAt             string
			payloadSHA256, errorText          string
			peerID                            int64
			fetchedCount, eventCount          int
			insertedCount, checkpointBefore   int
			checkpointAfter, backlogDetected  int
		)
		if err := rows.Scan(
			&batchID,
			&source,
			&peerType,
			&peerID,
			&status,
			&startedAt,
			&finishedAt,
			&fetchedCount,
			&eventCount,
			&insertedCount,
			&checkpointBefore,
			&checkpointAfter,
			&backlogDetected,
			&payloadSHA256,
			&errorText,
		); err != nil {
			_ = rows.Close()
			return summary, fmt.Errorf("scan account destruction batch: %w", err)
		}
		if _, err := fmt.Fprintf(
			batchDigest,
			"%s\x00%s\x00%s\x00%d\x00%s\x00%s\x00%s\x00%d\x00%d\x00%d\x00%d\x00%d\x00%d\x00%s\x00%s\n",
			batchID,
			source,
			peerType,
			peerID,
			status,
			startedAt,
			finishedAt,
			fetchedCount,
			eventCount,
			insertedCount,
			checkpointBefore,
			checkpointAfter,
			backlogDetected,
			payloadSHA256,
			errorText,
		); err != nil {
			_ = rows.Close()
			return summary, fmt.Errorf("hash account destruction batch: %w", err)
		}
		summary.IngestionBatchCount++
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return summary, fmt.Errorf("iterate account destruction batches: %w", err)
	}
	if err := rows.Close(); err != nil {
		return summary, fmt.Errorf("close account destruction batches: %w", err)
	}
	summary.BatchDigest = hex.EncodeToString(batchDigest.Sum(nil))
	return summary, nil
}

func (s *Service) CheckAccountPurge(ctx context.Context, accountID int64) error {
	if accountID <= 0 {
		return ErrNotFound
	}
	var holdID int64
	err := s.db.QueryRowContext(ctx, `
SELECT id
FROM legal_holds
WHERE account_id = ? AND released_at = ''
LIMIT 1`, accountID).Scan(&holdID)
	if err == nil {
		return ErrActiveLegalHold
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check account legal holds: %w", err)
	}
	return nil
}

func (s *Service) groupAccountIDs(ctx context.Context, groupID int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT account_id FROM account_group_members
WHERE group_id = ?
ORDER BY account_id`, groupID)
	if err != nil {
		return nil, fmt.Errorf("list account group members: %w", err)
	}
	defer func() { _ = rows.Close() }()
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan account group member: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate account group members: %w", err)
	}
	return ids, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanGroup(row scanner) (Group, error) {
	var item Group
	var createdAt, updatedAt string
	if err := row.Scan(&item.ID, &item.Name, &item.Color, &createdAt, &updatedAt); err != nil {
		return Group{}, fmt.Errorf("scan account group: %w", err)
	}
	var err error
	item.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return Group{}, fmt.Errorf("parse account group created_at: %w", err)
	}
	item.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return Group{}, fmt.Errorf("parse account group updated_at: %w", err)
	}
	item.AccountIDs = []int64{}
	return item, nil
}

func scanRule(row scanner) (Rule, error) {
	var (
		item                 Rule
		keywordsJSON         string
		excludeKeywordsJSON  string
		enabled              int
		lastRunAt            string
		createdAt, updatedAt string
	)
	if err := row.Scan(
		&item.ID,
		&item.Name,
		&item.AccountID,
		&item.PeerType,
		&item.PeerID,
		&item.PeerTitle,
		&keywordsJSON,
		&excludeKeywordsJSON,
		&item.Owner,
		&item.Version,
		&item.CreatedBy,
		&item.CooldownSeconds,
		&item.NotificationChannelID,
		&item.NotificationChannelName,
		&item.NotificationChannelKind,
		&enabled,
		&item.LastMessageID,
		&lastRunAt,
		&item.LastError,
		&createdAt,
		&updatedAt,
	); err != nil {
		return Rule{}, fmt.Errorf("scan monitor rule: %w", err)
	}
	item.Enabled = enabled != 0
	if err := json.Unmarshal([]byte(keywordsJSON), &item.Keywords); err != nil {
		return Rule{}, fmt.Errorf("decode monitor rule keywords: %w", err)
	}
	if err := json.Unmarshal([]byte(excludeKeywordsJSON), &item.ExcludeKeywords); err != nil {
		return Rule{}, fmt.Errorf("decode monitor rule exclude keywords: %w", err)
	}
	if lastRunAt != "" {
		value, err := time.Parse(time.RFC3339Nano, lastRunAt)
		if err != nil {
			return Rule{}, fmt.Errorf("parse monitor rule last_run_at: %w", err)
		}
		item.LastRunAt = &value
	}
	var err error
	item.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return Rule{}, fmt.Errorf("parse monitor rule created_at: %w", err)
	}
	item.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return Rule{}, fmt.Errorf("parse monitor rule updated_at: %w", err)
	}
	return item, nil
}

func scanAlert(row scanner) (Alert, error) {
	var item Alert
	var (
		createdAt, deliveryLastAt, deliveryAt, deliveryNextAt string
		acknowledgedAt, resolvedAt, updatedAt                 string
	)
	if err := row.Scan(
		&item.ID,
		&item.RuleID,
		&item.RuleName,
		&item.AccountID,
		&item.PeerType,
		&item.PeerID,
		&item.RuleVersion,
		&item.PeerTitle,
		&item.MessageID,
		&item.MatchedKeyword,
		&item.MessagePreview,
		&item.State,
		&item.ChannelID,
		&item.ChannelName,
		&createdAt,
		&item.DeliveryID,
		&item.DeliveryState,
		&item.DeliveryCount,
		&deliveryLastAt,
		&deliveryAt,
		&deliveryNextAt,
		&item.DeliveryError,
		&item.Severity,
		&item.AssignedTo,
		&item.AcknowledgedBy,
		&acknowledgedAt,
		&item.ResolvedBy,
		&resolvedAt,
		&updatedAt,
	); err != nil {
		return Alert{}, fmt.Errorf("scan monitor alert: %w", err)
	}
	value, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return Alert{}, fmt.Errorf("parse monitor alert created_at: %w", err)
	}
	item.CreatedAt = value
	if deliveryLastAt != "" {
		value, err := time.Parse(time.RFC3339Nano, deliveryLastAt)
		if err != nil {
			return Alert{}, fmt.Errorf("parse notification delivery attempt time: %w", err)
		}
		item.DeliveryLastAt = &value
	}
	if deliveryAt != "" {
		value, err := time.Parse(time.RFC3339Nano, deliveryAt)
		if err != nil {
			return Alert{}, fmt.Errorf("parse notification delivery time: %w", err)
		}
		item.DeliveryAt = &value
	}
	if deliveryNextAt != "" {
		value, err := time.Parse(time.RFC3339Nano, deliveryNextAt)
		if err != nil {
			return Alert{}, fmt.Errorf("parse notification delivery retry time: %w", err)
		}
		item.DeliveryNextAt = &value
	}
	if acknowledgedAt != "" {
		value, err := time.Parse(time.RFC3339Nano, acknowledgedAt)
		if err != nil {
			return Alert{}, fmt.Errorf("parse monitor alert acknowledgement time: %w", err)
		}
		item.AcknowledgedAt = &value
	}
	if resolvedAt != "" {
		value, err := time.Parse(time.RFC3339Nano, resolvedAt)
		if err != nil {
			return Alert{}, fmt.Errorf("parse monitor alert resolution time: %w", err)
		}
		item.ResolvedAt = &value
	}
	updated, err := time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return Alert{}, fmt.Errorf("parse monitor alert updated_at: %w", err)
	}
	item.UpdatedAt = updated
	return item, nil
}

func scanIndexedMessage(row scanner) (IndexedMessage, error) {
	var item IndexedMessage
	var sentAt, indexedAt string
	if err := row.Scan(
		&item.AccountID,
		&item.PeerType,
		&item.PeerID,
		&item.PeerTitle,
		&item.MessageID,
		&item.Sender,
		&item.Text,
		&sentAt,
		&indexedAt,
	); err != nil {
		return IndexedMessage{}, fmt.Errorf("scan indexed message: %w", err)
	}
	var err error
	item.SentAt, err = time.Parse(time.RFC3339Nano, sentAt)
	if err != nil {
		return IndexedMessage{}, fmt.Errorf("parse indexed message sent_at: %w", err)
	}
	item.IndexedAt, err = time.Parse(time.RFC3339Nano, indexedAt)
	if err != nil {
		return IndexedMessage{}, fmt.Errorf("parse indexed message indexed_at: %w", err)
	}
	return item, nil
}

type ruleQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func validateRuleChannel(ctx context.Context, queryer ruleQueryer, id int64) error {
	var (
		enabled int
		kind    string
	)
	err := queryer.QueryRowContext(
		ctx,
		`SELECT enabled, kind FROM notification_channels WHERE id = ?`,
		id,
	).Scan(&enabled, &kind)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("notification channel was not found")
	}
	if err != nil {
		return fmt.Errorf("read notification channel: %w", err)
	}
	if enabled == 0 {
		return errors.New("notification channel is disabled")
	}
	if kind != NotificationKindInApp && kind != NotificationKindWebhook {
		return errors.New("notification channel kind is not supported")
	}
	return nil
}

func insertRuleVersionTx(
	ctx context.Context,
	tx *sql.Tx,
	rule Rule,
	actor string,
	now time.Time,
) error {
	keywords, err := json.Marshal(rule.Keywords)
	if err != nil {
		return fmt.Errorf("encode monitor rule version keywords: %w", err)
	}
	excludes, err := json.Marshal(rule.ExcludeKeywords)
	if err != nil {
		return fmt.Errorf("encode monitor rule version exclude keywords: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO monitor_rule_versions (
	rule_id, version, name, account_id, peer_type, peer_id, peer_title,
	keywords_json, exclude_keywords_json, cooldown_seconds,
	notification_channel_id, owner, enabled, actor, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rule.ID,
		rule.Version,
		rule.Name,
		rule.AccountID,
		rule.PeerType,
		rule.PeerID,
		rule.PeerTitle,
		string(keywords),
		string(excludes),
		rule.CooldownSeconds,
		rule.NotificationChannelID,
		rule.Owner,
		boolInt(rule.Enabled),
		truncate(actor, 64),
		now.UTC().Format(time.RFC3339Nano),
	); err != nil {
		return fmt.Errorf("record monitor rule version: %w", err)
	}
	return nil
}

func normalizeRuleTerms(terms []string, label string) ([]string, error) {
	if len(terms) > 50 {
		return nil, fmt.Errorf("rule %s list must contain at most 50 items", label)
	}
	seen := make(map[string]struct{}, len(terms))
	normalized := make([]string, 0, len(terms))
	for _, term := range terms {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		if utf8.RuneCountInString(term) > 80 {
			return nil, fmt.Errorf("rule %s must contain at most 80 characters", label)
		}
		key := strings.ToLower(term)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, term)
	}
	sort.Strings(normalized)
	return normalized, nil
}

func validateRuleTermsDoNotOverlap(keywords, excludes []string) error {
	excluded := make(map[string]struct{}, len(excludes))
	for _, term := range excludes {
		excluded[strings.ToLower(term)] = struct{}{}
	}
	for _, keyword := range keywords {
		if _, exists := excluded[strings.ToLower(keyword)]; exists {
			return fmt.Errorf("keyword %q cannot also be an exclude keyword", keyword)
		}
	}
	return nil
}

func matchedKeyword(keywords []string, text string) string {
	if len(keywords) == 0 {
		return ""
	}
	lowerText := strings.ToLower(text)
	for _, keyword := range keywords {
		if strings.Contains(lowerText, strings.ToLower(keyword)) {
			return keyword
		}
	}
	return ""
}

func matchesRule(rule Rule, text string) (string, bool) {
	keyword := matchedKeyword(rule.Keywords, text)
	if len(rule.Keywords) > 0 && keyword == "" {
		return "", false
	}
	if matchedKeyword(rule.ExcludeKeywords, text) != "" {
		return "", false
	}
	return keyword, true
}

func normalizeColor(color string) string {
	switch strings.ToLower(strings.TrimSpace(color)) {
	case "green", "orange", "red", "purple":
		return strings.ToLower(strings.TrimSpace(color))
	default:
		return "blue"
	}
}

func normalizeLimit(value, fallback, maximum int) int {
	if value <= 0 {
		value = fallback
	}
	if value > maximum {
		value = maximum
	}
	return value
}

func truncate(value string, maximum int) string {
	if maximum <= 0 || utf8.RuneCountInString(value) <= maximum {
		return value
	}
	runes := []rune(value)
	return string(runes[:maximum])
}

func escapeLike(value string) string {
	replacer := strings.NewReplacer(`!`, `!!`, `%`, `!%`, `_`, `!_`)
	return replacer.Replace(value)
}

func ftsPhrase(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
