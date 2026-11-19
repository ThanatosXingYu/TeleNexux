package operations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	CorrelationLink    = "link"
	CorrelationDomain  = "domain"
	CorrelationSender  = "sender"
	CorrelationContent = "content"

	correlationMessageLimit = 5000
	correlationSourceLimit  = 20
	correlationCaveat       = "关联结果仅用于发现线索，不代表账号属于同一自然人或组织；链接、域名、发送者显示名及相同文本均可能存在误报。"
)

var correlationURLPattern = regexp.MustCompile("(?i)https?://[^\\s<>\"'`]+")

var ErrInvalidCorrelationQuery = errors.New("invalid correlation query")

type CorrelationQuery struct {
	Days  int
	Types []string
	Limit int
}

type CorrelationSource struct {
	AccountID     int64     `json:"account_id"`
	PeerType      string    `json:"peer_type"`
	PeerID        int64     `json:"peer_id"`
	PeerTitle     string    `json:"peer_title"`
	MessageID     int       `json:"message_id"`
	Sender        string    `json:"sender"`
	TextPreview   string    `json:"text_preview"`
	SentAt        time.Time `json:"sent_at"`
	EvidenceID    string    `json:"evidence_event_id"`
	ContentSHA256 string    `json:"content_sha256"`
}

type CorrelationGroup struct {
	ID               string              `json:"id"`
	Type             string              `json:"type"`
	Match            string              `json:"match"`
	Confidence       float64             `json:"confidence"`
	ConfidenceLabel  string              `json:"confidence_label"`
	ConfidenceReason string              `json:"confidence_reason"`
	AccountCount     int                 `json:"account_count"`
	SourceCount      int                 `json:"source_count"`
	Sources          []CorrelationSource `json:"sources"`
	SourcesTruncated bool                `json:"sources_truncated"`
}

type CorrelationAnalysis struct {
	WindowDays    int                `json:"window_days"`
	Since         time.Time          `json:"since"`
	AnalyzedAt    time.Time          `json:"analyzed_at"`
	MessageCount  int                `json:"message_count"`
	MessageLimit  int                `json:"message_limit"`
	Truncated     bool               `json:"truncated"`
	Types         []string           `json:"types"`
	TotalGroups   int                `json:"total_groups"`
	Groups        []CorrelationGroup `json:"groups"`
	GoverningNote string             `json:"governing_note"`
}

type correlationMessage struct {
	CorrelationSource
	Text string
}

type correlationAccumulator struct {
	group    CorrelationGroup
	accounts map[int64]struct{}
}

type correlationMatch struct {
	kind       string
	key        string
	display    string
	confidence float64
	label      string
	reason     string
}

func (s *Service) AnalyzeCorrelations(
	ctx context.Context,
	input CorrelationQuery,
) (CorrelationAnalysis, error) {
	windowDays, types, limit, err := normalizeCorrelationQuery(input)
	if err != nil {
		return CorrelationAnalysis{}, err
	}
	now := s.now().UTC()
	since := now.AddDate(0, 0, -windowDays)
	rows, err := s.db.QueryContext(ctx, `
SELECT account_id, peer_type, peer_id, peer_title, message_id, sender, text,
	sent_at, latest_event_id, content_hash
FROM indexed_messages
WHERE deleted_at = '' AND sent_at >= ?
ORDER BY sent_at DESC, account_id, peer_id, message_id
LIMIT ?`,
		since.Format(time.RFC3339Nano),
		correlationMessageLimit+1,
	)
	if err != nil {
		return CorrelationAnalysis{}, fmt.Errorf("load correlation messages: %w", err)
	}
	defer func() { _ = rows.Close() }()
	messages := make([]correlationMessage, 0, correlationMessageLimit)
	truncated := false
	for rows.Next() {
		message, err := scanCorrelationMessage(rows)
		if err != nil {
			return CorrelationAnalysis{}, err
		}
		if len(messages) == correlationMessageLimit {
			truncated = true
			break
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return CorrelationAnalysis{}, fmt.Errorf("iterate correlation messages: %w", err)
	}

	accumulators := accumulateCorrelations(messages, types)
	groups := finalizeCorrelationGroups(accumulators)
	totalGroups := len(groups)
	if len(groups) > limit {
		groups = groups[:limit]
	}
	return CorrelationAnalysis{
		WindowDays:    windowDays,
		Since:         since,
		AnalyzedAt:    now,
		MessageCount:  len(messages),
		MessageLimit:  correlationMessageLimit,
		Truncated:     truncated,
		Types:         types,
		TotalGroups:   totalGroups,
		Groups:        groups,
		GoverningNote: correlationCaveat,
	}, nil
}

func normalizeCorrelationQuery(
	input CorrelationQuery,
) (int, []string, int, error) {
	if input.Days < 1 {
		input.Days = 30
	}
	if input.Days > 90 {
		input.Days = 90
	}
	input.Limit = normalizeLimit(input.Limit, 50, 100)
	requested := make(map[string]struct{}, len(input.Types))
	for _, value := range input.Types {
		for _, item := range strings.Split(value, ",") {
			item = strings.ToLower(strings.TrimSpace(item))
			if item == "" {
				continue
			}
			switch item {
			case CorrelationLink, CorrelationDomain, CorrelationSender, CorrelationContent:
				requested[item] = struct{}{}
			default:
				return 0, nil, 0, fmt.Errorf(
					"%w: unsupported correlation type %q",
					ErrInvalidCorrelationQuery,
					item,
				)
			}
		}
	}
	types := make([]string, 0, 4)
	for _, item := range []string{
		CorrelationLink,
		CorrelationContent,
		CorrelationDomain,
		CorrelationSender,
	} {
		if len(requested) == 0 {
			types = append(types, item)
			continue
		}
		if _, ok := requested[item]; ok {
			types = append(types, item)
		}
	}
	return input.Days, types, input.Limit, nil
}

func scanCorrelationMessage(row scanner) (correlationMessage, error) {
	var (
		message correlationMessage
		sentAt  string
	)
	if err := row.Scan(
		&message.AccountID,
		&message.PeerType,
		&message.PeerID,
		&message.PeerTitle,
		&message.MessageID,
		&message.Sender,
		&message.Text,
		&sentAt,
		&message.EvidenceID,
		&message.ContentSHA256,
	); err != nil {
		return correlationMessage{}, fmt.Errorf("scan correlation message: %w", err)
	}
	message.TextPreview = truncate(strings.Join(strings.Fields(message.Text), " "), 180)
	value, err := time.Parse(time.RFC3339Nano, sentAt)
	if err != nil {
		return correlationMessage{}, fmt.Errorf("parse correlation message time: %w", err)
	}
	message.SentAt = value
	return message, nil
}

func accumulateCorrelations(
	messages []correlationMessage,
	types []string,
) map[string]*correlationAccumulator {
	enabled := make(map[string]struct{}, len(types))
	for _, item := range types {
		enabled[item] = struct{}{}
	}
	accumulators := make(map[string]*correlationAccumulator)
	for _, message := range messages {
		seen := make(map[string]struct{})
		for _, match := range messageCorrelationMatches(message, enabled) {
			groupKey := match.kind + "\x00" + match.key
			if _, exists := seen[groupKey]; exists {
				continue
			}
			seen[groupKey] = struct{}{}
			accumulator := accumulators[groupKey]
			if accumulator == nil {
				accumulator = &correlationAccumulator{
					group: CorrelationGroup{
						ID:               correlationGroupID(groupKey),
						Type:             match.kind,
						Match:            truncate(match.display, 240),
						Confidence:       match.confidence,
						ConfidenceLabel:  match.label,
						ConfidenceReason: match.reason,
					},
					accounts: make(map[int64]struct{}),
				}
				accumulators[groupKey] = accumulator
			}
			accumulator.group.SourceCount++
			accumulator.accounts[message.AccountID] = struct{}{}
			if len(accumulator.group.Sources) < correlationSourceLimit {
				accumulator.group.Sources = append(accumulator.group.Sources, message.CorrelationSource)
			} else {
				accumulator.group.SourcesTruncated = true
			}
		}
	}
	return accumulators
}

func messageCorrelationMatches(
	message correlationMessage,
	enabled map[string]struct{},
) []correlationMatch {
	matches := make([]correlationMatch, 0, 4)
	links, domains := correlationLinks(message.Text)
	if _, ok := enabled[CorrelationLink]; ok {
		for _, link := range links {
			matches = append(matches, correlationMatch{
				kind: CorrelationLink, key: link, display: link,
				confidence: 0.9, label: "高", reason: "不同账号出现同一规范化链接。",
			})
		}
	}
	if _, ok := enabled[CorrelationDomain]; ok {
		for _, domain := range domains {
			matches = append(matches, correlationMatch{
				kind: CorrelationDomain, key: domain, display: domain,
				confidence: 0.55, label: "中", reason: "不同账号出现同一域名，可能只是常用平台。",
			})
		}
	}
	if sender := normalizeCorrelationText(message.Sender); validCorrelationSender(sender) {
		if _, ok := enabled[CorrelationSender]; ok {
			matches = append(matches, correlationMatch{
				kind: CorrelationSender, key: sender, display: strings.TrimSpace(message.Sender),
				confidence: 0.35, label: "低", reason: "仅发送者显示名相同，不能认定为同一用户。",
			})
		}
	}
	if content := normalizeCorrelationText(message.Text); utf8.RuneCountInString(content) >= 20 {
		if _, ok := enabled[CorrelationContent]; ok {
			matches = append(matches, correlationMatch{
				kind: CorrelationContent, key: truncate(content, 4000), display: truncate(content, 160),
				confidence: 0.75, label: "中高", reason: "不同账号出现相同文本，可能是转发或重复内容。",
			})
		}
	}
	return matches
}

func correlationLinks(text string) ([]string, []string) {
	links := make([]string, 0, 2)
	domains := make([]string, 0, 2)
	seenLinks := make(map[string]struct{})
	seenDomains := make(map[string]struct{})
	for _, raw := range correlationURLPattern.FindAllString(text, -1) {
		normalized, domain := normalizeCorrelationURL(raw)
		if normalized == "" || domain == "" {
			continue
		}
		if _, exists := seenLinks[normalized]; !exists {
			seenLinks[normalized] = struct{}{}
			links = append(links, normalized)
		}
		if _, exists := seenDomains[domain]; !exists {
			seenDomains[domain] = struct{}{}
			domains = append(domains, domain)
		}
	}
	return links, domains
}

func normalizeCorrelationURL(raw string) (string, string) {
	raw = strings.TrimRight(strings.TrimSpace(raw), ".,;:!?)]}，。；：！？、")
	if raw == "" || len(raw) > 2048 {
		return "", ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", ""
	}
	host := strings.TrimPrefix(strings.ToLower(parsed.Hostname()), "www.")
	if host == "" {
		return "", ""
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = host
	parsed.Fragment = ""
	query := parsed.Query()
	for key := range query {
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "utm_") ||
			lower == "fbclid" || lower == "gclid" ||
			lower == "yclid" || lower == "mc_cid" || lower == "mc_eid" {
			query.Del(key)
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), host
}

func normalizeCorrelationText(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func validCorrelationSender(value string) bool {
	if utf8.RuneCountInString(value) < 4 {
		return false
	}
	switch strings.ToLower(value) {
	case "未知用户", "系统", "unknown", "system":
		return false
	}
	return true
}

func finalizeCorrelationGroups(
	accumulators map[string]*correlationAccumulator,
) []CorrelationGroup {
	groups := make([]CorrelationGroup, 0, len(accumulators))
	for _, accumulator := range accumulators {
		if len(accumulator.accounts) < 2 {
			continue
		}
		group := accumulator.group
		group.AccountCount = len(accumulator.accounts)
		groups = append(groups, group)
	}
	sort.Slice(groups, func(left, right int) bool {
		if groups[left].Confidence != groups[right].Confidence {
			return groups[left].Confidence > groups[right].Confidence
		}
		if groups[left].AccountCount != groups[right].AccountCount {
			return groups[left].AccountCount > groups[right].AccountCount
		}
		if groups[left].SourceCount != groups[right].SourceCount {
			return groups[left].SourceCount > groups[right].SourceCount
		}
		if groups[left].Type != groups[right].Type {
			return groups[left].Type < groups[right].Type
		}
		return groups[left].Match < groups[right].Match
	})
	return groups
}

func correlationGroupID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:12])
}
