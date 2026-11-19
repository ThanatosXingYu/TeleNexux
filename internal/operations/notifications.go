package operations

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	NotificationKindInApp   = "in_app"
	NotificationKindWebhook = "webhook"
	notificationMaxAttempts = 5
	notificationClaimLease  = 2 * time.Minute
)

type WebhookChannelConfig struct {
	URL    string `json:"url"`
	Secret string `json:"secret,omitempty"`
}

type NotificationDelivery struct {
	ID             int64
	AlertID        int64
	ChannelID      int64
	ChannelKind    string
	ChannelName    string
	ChannelConfig  string
	AttemptCount   int
	NextAttemptAt  *time.Time
	RuleID         int64
	RuleName       string
	AccountID      int64
	PeerType       string
	PeerID         int64
	PeerTitle      string
	MessageID      int
	MatchedKeyword string
	MessagePreview string
	Severity       string
	AlertCreatedAt time.Time
}

func (s *Service) CreateWebhookChannel(
	ctx context.Context,
	name string,
	rawURL string,
	secret string,
) (NotificationChannel, error) {
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 60 {
		return NotificationChannel{}, errors.New("通知渠道名称需为 1 到 60 个字符")
	}
	rawURL = strings.TrimSpace(rawURL)
	if err := validateWebhookURLSyntax(rawURL); err != nil {
		return NotificationChannel{}, err
	}
	secret = strings.TrimSpace(secret)
	if secret != "" && (utf8.RuneCountInString(secret) < 16 ||
		utf8.RuneCountInString(secret) > 128) {
		return NotificationChannel{}, errors.New("Webhook 签名密钥需为 16 到 128 个字符")
	}
	config, err := json.Marshal(WebhookChannelConfig{URL: rawURL, Secret: secret})
	if err != nil {
		return NotificationChannel{}, fmt.Errorf("encode webhook configuration: %w", err)
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `
INSERT INTO notification_channels (
	name, kind, enabled, config_json, created_at, updated_at
) VALUES (?, 'webhook', 1, ?, ?, ?)`,
		name,
		string(config),
		now,
		now,
	)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return NotificationChannel{}, errors.New("通知渠道名称已存在")
		}
		return NotificationChannel{}, fmt.Errorf("create notification channel: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return NotificationChannel{}, fmt.Errorf("read notification channel ID: %w", err)
	}
	item := NotificationChannel{
		ID:         id,
		Name:       name,
		Kind:       NotificationKindWebhook,
		Enabled:    true,
		Configured: true,
	}
	item.CreatedAt, _ = time.Parse(time.RFC3339Nano, now)
	item.UpdatedAt = item.CreatedAt
	return item, nil
}

func (s *Service) ClaimNotificationDelivery(
	ctx context.Context,
) (NotificationDelivery, bool, error) {
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return NotificationDelivery{}, false, fmt.Errorf("begin notification claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var (
		item                             NotificationDelivery
		nextAttemptAt, createdAt         string
		alertID                          int64
		channelID                        int64
		ruleID, accountID, peerID        int64
		messageID, attemptCount          int
		channelKind, channelName, config string
		ruleName, peerType, peerTitle    string
		matchedKeyword, messagePreview   string
		severity                         string
	)
	err = tx.QueryRowContext(ctx, `
SELECT d.id, d.alert_id, d.channel_id, d.channel_kind, nc.name, nc.config_json,
	d.attempt_count, d.next_attempt_at, a.rule_id, COALESCE(r.name, ''),
	a.account_id, a.peer_type, a.peer_id, a.peer_title, a.message_id,
	a.matched_keyword, a.message_preview, a.severity, a.created_at
FROM notification_deliveries d
JOIN notification_channels nc ON nc.id = d.channel_id AND nc.enabled = 1
JOIN monitor_alerts a ON a.id = d.alert_id
LEFT JOIN monitor_rules r ON r.id = a.rule_id
WHERE d.state = 'queued'
	AND (d.next_attempt_at = '' OR d.next_attempt_at <= ?)
ORDER BY
	CASE WHEN d.next_attempt_at = '' THEN d.created_at ELSE d.next_attempt_at END,
	d.id
LIMIT 1`,
		now.Format(time.RFC3339Nano),
	).Scan(
		&item.ID,
		&alertID,
		&channelID,
		&channelKind,
		&channelName,
		&config,
		&attemptCount,
		&nextAttemptAt,
		&ruleID,
		&ruleName,
		&accountID,
		&peerType,
		&peerID,
		&peerTitle,
		&messageID,
		&matchedKeyword,
		&messagePreview,
		&severity,
		&createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return NotificationDelivery{}, false, nil
	}
	if err != nil {
		return NotificationDelivery{}, false, fmt.Errorf("select notification delivery: %w", err)
	}
	leaseUntil := now.Add(notificationClaimLease)
	result, err := tx.ExecContext(ctx, `
UPDATE notification_deliveries
SET attempt_count = attempt_count + 1,
	last_attempt_at = ?,
	next_attempt_at = ?,
	updated_at = ?
WHERE id = ? AND state = 'queued' AND attempt_count = ?`,
		now.Format(time.RFC3339Nano),
		leaseUntil.Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano),
		item.ID,
		attemptCount,
	)
	if err != nil {
		return NotificationDelivery{}, false, fmt.Errorf("claim notification delivery: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return NotificationDelivery{}, false, fmt.Errorf("check notification claim: %w", err)
	}
	if affected == 0 {
		return NotificationDelivery{}, false, nil
	}
	if nextAttemptAt != "" {
		value, parseErr := time.Parse(time.RFC3339Nano, nextAttemptAt)
		if parseErr != nil {
			return NotificationDelivery{}, false,
				fmt.Errorf("parse notification retry time: %w", parseErr)
		}
		item.NextAttemptAt = &value
	}
	item.AlertID = alertID
	item.ChannelID = channelID
	item.ChannelKind = channelKind
	item.ChannelName = channelName
	item.ChannelConfig = config
	item.AttemptCount = attemptCount + 1
	item.RuleID = ruleID
	item.RuleName = ruleName
	item.AccountID = accountID
	item.PeerType = peerType
	item.PeerID = peerID
	item.PeerTitle = peerTitle
	item.MessageID = messageID
	item.MatchedKeyword = matchedKeyword
	item.MessagePreview = messagePreview
	item.Severity = severity
	item.AlertCreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return NotificationDelivery{}, false,
			fmt.Errorf("parse notification alert time: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return NotificationDelivery{}, false, fmt.Errorf("commit notification claim: %w", err)
	}
	return item, true, nil
}

func (s *Service) CompleteNotificationDelivery(ctx context.Context, id int64) error {
	if id <= 0 {
		return errors.New("notification delivery ID must be positive")
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `
UPDATE notification_deliveries
SET state = 'delivered', delivered_at = ?, next_attempt_at = '',
	error_text = '', updated_at = ?
WHERE id = ? AND state = 'queued'`,
		now,
		now,
		id,
	)
	if err != nil {
		return fmt.Errorf("complete notification delivery: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check completed notification delivery: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Service) RetryNotificationDelivery(
	ctx context.Context,
	id int64,
	errorText string,
) (string, *time.Time, error) {
	if id <= 0 {
		return "", nil, errors.New("notification delivery ID must be positive")
	}
	var attemptCount int
	var state string
	err := s.db.QueryRowContext(ctx, `
SELECT attempt_count, state FROM notification_deliveries WHERE id = ?`, id,
	).Scan(&attemptCount, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, ErrNotFound
	}
	if err != nil {
		return "", nil, fmt.Errorf("read notification delivery retry state: %w", err)
	}
	if state != "queued" {
		return state, nil, nil
	}
	now := s.now().UTC()
	nextState := "queued"
	nextAttempt := now.Add(notificationRetryDelay(attemptCount))
	nextAttemptText := nextAttempt.Format(time.RFC3339Nano)
	if attemptCount >= notificationMaxAttempts {
		nextState = "failed"
		nextAttempt = time.Time{}
		nextAttemptText = ""
	}
	errorText = truncate(strings.TrimSpace(errorText), 300)
	result, err := s.db.ExecContext(ctx, `
UPDATE notification_deliveries
SET state = ?, next_attempt_at = ?, error_text = ?, updated_at = ?
WHERE id = ? AND state = 'queued' AND attempt_count = ?`,
		nextState,
		nextAttemptText,
		errorText,
		now.Format(time.RFC3339Nano),
		id,
		attemptCount,
	)
	if err != nil {
		return "", nil, fmt.Errorf("retry notification delivery: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return "", nil, fmt.Errorf("check notification retry: %w", err)
	}
	if affected == 0 {
		return "", nil, ErrNotFound
	}
	if nextState == "failed" {
		return nextState, nil, nil
	}
	return nextState, &nextAttempt, nil
}

func notificationRetryDelay(attemptCount int) time.Duration {
	switch {
	case attemptCount <= 1:
		return 30 * time.Second
	case attemptCount == 2:
		return 2 * time.Minute
	case attemptCount == 3:
		return 8 * time.Minute
	default:
		return 30 * time.Minute
	}
}

func validateWebhookURLSyntax(rawURL string) error {
	if rawURL == "" || utf8.RuneCountInString(rawURL) > 2048 {
		return errors.New("Webhook 地址不能为空且不能超过 2048 个字符")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" || parsed.Hostname() == "" {
		return errors.New("Webhook 地址格式无效")
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return errors.New("Webhook 地址必须使用 HTTP 或 HTTPS")
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return errors.New("Webhook 地址不得包含用户信息或片段")
	}
	return nil
}

func notificationConfigConfigured(raw string) bool {
	var config WebhookChannelConfig
	return json.Unmarshal([]byte(raw), &config) == nil && config.URL != ""
}
