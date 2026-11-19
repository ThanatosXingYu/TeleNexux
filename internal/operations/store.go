// Package operations stores account groups, monitor rules, alerts, and the message index.
package operations

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("operations record not found")

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
	ID            int64      `json:"id"`
	Name          string     `json:"name"`
	AccountID     int64      `json:"account_id"`
	PeerType      string     `json:"peer_type"`
	PeerID        int64      `json:"peer_id"`
	PeerTitle     string     `json:"peer_title"`
	Keywords      []string   `json:"keywords"`
	Enabled       bool       `json:"enabled"`
	LastMessageID int        `json:"last_message_id"`
	LastRunAt     *time.Time `json:"last_run_at,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type Alert struct {
	ID             int64     `json:"id"`
	RuleID         int64     `json:"rule_id"`
	RuleName       string    `json:"rule_name"`
	AccountID      int64     `json:"account_id"`
	PeerType       string    `json:"peer_type"`
	PeerID         int64     `json:"peer_id"`
	PeerTitle      string    `json:"peer_title"`
	MessageID      int       `json:"message_id"`
	MatchedKeyword string    `json:"matched_keyword,omitempty"`
	MessagePreview string    `json:"message_preview"`
	State          string    `json:"state"`
	CreatedAt      time.Time `json:"created_at"`
}

type IndexedMessage struct {
	AccountID int64     `json:"account_id"`
	PeerType  string    `json:"peer_type"`
	PeerID    int64     `json:"peer_id"`
	PeerTitle string    `json:"peer_title"`
	MessageID int       `json:"message_id"`
	Sender    string    `json:"sender"`
	Text      string    `json:"text"`
	SentAt    time.Time `json:"sent_at"`
	IndexedAt time.Time `json:"indexed_at"`
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
	db  *sql.DB
	now func() time.Time
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
CREATE TABLE IF NOT EXISTS monitor_rules (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL,
	account_id INTEGER NOT NULL,
	peer_type TEXT NOT NULL,
	peer_id INTEGER NOT NULL,
	peer_title TEXT NOT NULL DEFAULT '',
	keywords_json TEXT NOT NULL DEFAULT '[]',
	enabled INTEGER NOT NULL DEFAULT 1,
	last_message_id INTEGER NOT NULL DEFAULT 0,
	last_run_at TEXT NOT NULL DEFAULT '',
	last_error TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS monitor_rules_enabled_idx
	ON monitor_rules(enabled, account_id, id);
CREATE TABLE IF NOT EXISTS monitor_alerts (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	rule_id INTEGER NOT NULL,
	account_id INTEGER NOT NULL,
	peer_type TEXT NOT NULL,
	peer_id INTEGER NOT NULL,
	peer_title TEXT NOT NULL DEFAULT '',
	message_id INTEGER NOT NULL,
	matched_keyword TEXT NOT NULL DEFAULT '',
	message_preview TEXT NOT NULL DEFAULT '',
	state TEXT NOT NULL DEFAULT 'pending',
	created_at TEXT NOT NULL,
	UNIQUE(rule_id, message_id)
);
CREATE INDEX IF NOT EXISTS monitor_alerts_state_created_idx
	ON monitor_alerts(state, created_at DESC, id DESC);
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
	ON indexed_messages(account_id, peer_type, peer_id, message_id DESC);`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate operations database: %w", err)
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
	name = strings.TrimSpace(name)
	peerTitle = strings.TrimSpace(peerTitle)
	peerType = strings.ToLower(strings.TrimSpace(peerType))
	keywords = normalizeKeywords(keywords)
	if utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 60 {
		return Rule{}, errors.New("rule name must contain 1 to 60 characters")
	}
	if accountID <= 0 || peerID <= 0 {
		return Rule{}, errors.New("account ID and chat ID must be positive")
	}
	if peerType != "user" && peerType != "chat" && peerType != "channel" {
		return Rule{}, errors.New("peer type must be user, chat, or channel")
	}
	if utf8.RuneCountInString(peerTitle) > 120 {
		return Rule{}, errors.New("peer title must contain at most 120 characters")
	}
	encoded, err := json.Marshal(keywords)
	if err != nil {
		return Rule{}, fmt.Errorf("encode monitor keywords: %w", err)
	}
	now := s.now().UTC()
	result, err := s.db.ExecContext(ctx, `
INSERT INTO monitor_rules (
	name, account_id, peer_type, peer_id, peer_title, keywords_json, enabled,
	last_message_id, last_run_at, last_error, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, 1, 0, '', '', ?, ?)`,
		name,
		accountID,
		peerType,
		peerID,
		peerTitle,
		string(encoded),
		now.Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano),
	)
	if err != nil {
		return Rule{}, fmt.Errorf("create monitor rule: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Rule{}, fmt.Errorf("read monitor rule ID: %w", err)
	}
	return s.GetRule(ctx, id)
}

func (s *Service) GetRule(ctx context.Context, id int64) (Rule, error) {
	rule, err := scanRule(s.db.QueryRowContext(ctx, `
SELECT id, name, account_id, peer_type, peer_id, peer_title, keywords_json, enabled,
	last_message_id, last_run_at, last_error, created_at, updated_at
FROM monitor_rules WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Rule{}, ErrNotFound
	}
	return rule, err
}

func (s *Service) ListRules(ctx context.Context, enabledOnly bool) ([]Rule, error) {
	query := `
SELECT id, name, account_id, peer_type, peer_id, peer_title, keywords_json, enabled,
	last_message_id, last_run_at, last_error, created_at, updated_at
FROM monitor_rules`
	if enabledOnly {
		query += " WHERE enabled = 1"
	}
	query += " ORDER BY created_at DESC, id DESC"
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

func (s *Service) SetRuleEnabled(ctx context.Context, id int64, enabled bool) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE monitor_rules SET enabled = ?, updated_at = ? WHERE id = ?`,
		boolInt(enabled),
		s.now().UTC().Format(time.RFC3339Nano),
		id,
	)
	if err != nil {
		return fmt.Errorf("update monitor rule: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check updated monitor rule: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Service) DeleteRule(ctx context.Context, id int64) error {
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
	SentAt    time.Time
}

func (s *Service) IngestMessages(
	ctx context.Context,
	messages []MessageRecord,
) (int, []Alert, error) {
	if len(messages) == 0 {
		return 0, nil, nil
	}
	rules, err := s.ListRules(ctx, true)
	if err != nil {
		return 0, nil, err
	}
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, nil, fmt.Errorf("begin message ingestion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	inserted := 0
	alerts := make([]Alert, 0)
	for _, message := range messages {
		if message.AccountID <= 0 || message.PeerID <= 0 || message.MessageID <= 0 {
			continue
		}
		result, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO indexed_messages (
	account_id, peer_type, peer_id, peer_title, message_id, sender, text, sent_at, indexed_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			message.AccountID,
			message.PeerType,
			message.PeerID,
			truncate(message.PeerTitle, 120),
			message.MessageID,
			truncate(message.Sender, 120),
			truncate(message.Text, 16<<10),
			message.SentAt.UTC().Format(time.RFC3339Nano),
			now.Format(time.RFC3339Nano),
		)
		if err != nil {
			return 0, nil, fmt.Errorf("index Telegram message: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return 0, nil, fmt.Errorf("check indexed Telegram message: %w", err)
		}
		inserted += int(affected)

		for _, rule := range rules {
			if !rule.AppliesTo(message) || message.MessageID <= rule.LastMessageID {
				continue
			}
			keyword := matchedKeyword(rule.Keywords, message.Text)
			if len(rule.Keywords) > 0 && keyword == "" {
				continue
			}
			alert, err := s.insertAlert(ctx, tx, rule, message, keyword, now)
			if err != nil {
				return 0, nil, err
			}
			if alert.ID > 0 {
				alerts = append(alerts, alert)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, fmt.Errorf("commit message ingestion: %w", err)
	}
	return inserted, alerts, nil
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
	result, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO monitor_alerts (
	rule_id, account_id, peer_type, peer_id, peer_title, message_id,
	matched_keyword, message_preview, state, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?)`,
		rule.ID,
		rule.AccountID,
		rule.PeerType,
		rule.PeerID,
		truncate(title, 120),
		message.MessageID,
		truncate(keyword, 80),
		preview,
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
	return Alert{
		ID:             id,
		RuleID:         rule.ID,
		RuleName:       rule.Name,
		AccountID:      rule.AccountID,
		PeerType:       rule.PeerType,
		PeerID:         rule.PeerID,
		PeerTitle:      title,
		MessageID:      message.MessageID,
		MatchedKeyword: keyword,
		MessagePreview: preview,
		State:          "pending",
		CreatedAt:      now,
	}, nil
}

func (s *Service) ListAlerts(ctx context.Context, state string, limit int) ([]Alert, error) {
	if state != "" && state != "pending" && state != "resolved" {
		return nil, errors.New("alert state must be pending or resolved")
	}
	limit = normalizeLimit(limit, 50, 200)
	query := `
SELECT a.id, a.rule_id, COALESCE(r.name, ''), a.account_id, a.peer_type, a.peer_id,
	a.peer_title, a.message_id, a.matched_keyword, a.message_preview, a.state, a.created_at
FROM monitor_alerts a
LEFT JOIN monitor_rules r ON r.id = a.rule_id`
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

func (s *Service) ResolveAlert(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE monitor_alerts SET state = 'resolved' WHERE id = ? AND state = 'pending'`,
		id,
	)
	if err != nil {
		return fmt.Errorf("resolve monitor alert: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check resolved monitor alert: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
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
	where := make([]string, 0, 5)
	args := make([]any, 0, 6)
	if filter.Query != "" {
		where = append(where, "(text LIKE ? ESCAPE '!' OR sender LIKE ? ESCAPE '!' OR peer_title LIKE ? ESCAPE '!')")
		pattern := "%" + escapeLike(filter.Query) + "%"
		args = append(args, pattern, pattern, pattern)
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
WHERE sent_at >= ?
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
WHERE sent_at >= ?
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
		{dest: &result.AccountCount, query: `SELECT COUNT(1) FROM accounts`},
		{
			dest:  &result.OnlineCount,
			query: `SELECT COUNT(1) FROM accounts WHERE status = 'online' AND last_verified_at >= ?`,
			args:  []any{now.Add(-10 * time.Minute).Format(time.RFC3339Nano)},
		},
		{dest: &result.MonitorCount, query: `SELECT COUNT(1) FROM monitor_rules WHERE enabled = 1`},
		{
			dest:  &result.TodayMessages,
			query: `SELECT COUNT(1) FROM indexed_messages WHERE sent_at >= ?`,
			args:  []any{now.Truncate(24 * time.Hour).Format(time.RFC3339Nano)},
		},
		{
			dest:  &result.PendingAlerts,
			query: `SELECT COUNT(1) FROM monitor_alerts WHERE state = 'pending'`,
		},
	}
	for _, item := range queries {
		if err := s.db.QueryRowContext(ctx, item.query, item.args...).Scan(item.dest); err != nil {
			return Dashboard{}, fmt.Errorf("build dashboard statistics: %w", err)
		}
	}
	return result, nil
}

func (s *Service) CleanupMessages(ctx context.Context, retentionDays int) (int64, error) {
	if retentionDays < 1 {
		retentionDays = 30
	}
	if retentionDays > 3650 {
		retentionDays = 3650
	}
	cutoff := s.now().UTC().AddDate(0, 0, -retentionDays).Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `DELETE FROM indexed_messages WHERE sent_at < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("cleanup indexed messages: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count cleaned messages: %w", err)
	}
	return deleted, nil
}

func (s *Service) PurgeAccount(ctx context.Context, accountID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin purge account operations: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, query := range []string{
		`DELETE FROM account_group_members WHERE account_id = ?`,
		`DELETE FROM monitor_alerts WHERE account_id = ?`,
		`DELETE FROM monitor_rules WHERE account_id = ?`,
		`DELETE FROM indexed_messages WHERE account_id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, query, accountID); err != nil {
			return fmt.Errorf("purge account operations: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit purge account operations: %w", err)
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
	var createdAt string
	if err := row.Scan(
		&item.ID,
		&item.RuleID,
		&item.RuleName,
		&item.AccountID,
		&item.PeerType,
		&item.PeerID,
		&item.PeerTitle,
		&item.MessageID,
		&item.MatchedKeyword,
		&item.MessagePreview,
		&item.State,
		&createdAt,
	); err != nil {
		return Alert{}, fmt.Errorf("scan monitor alert: %w", err)
	}
	value, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return Alert{}, fmt.Errorf("parse monitor alert created_at: %w", err)
	}
	item.CreatedAt = value
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

func normalizeKeywords(keywords []string) []string {
	seen := make(map[string]struct{}, len(keywords))
	normalized := make([]string, 0, len(keywords))
	for _, keyword := range keywords {
		keyword = strings.TrimSpace(keyword)
		if keyword == "" {
			continue
		}
		keyword = truncate(keyword, 80)
		key := strings.ToLower(keyword)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, keyword)
	}
	sort.Strings(normalized)
	return normalized
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

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
