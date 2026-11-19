package operations

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

type AuditEvent struct {
	Sequence     int64     `json:"sequence"`
	RequestID    string    `json:"request_id"`
	Actor        string    `json:"actor"`
	Role         string    `json:"role"`
	Method       string    `json:"method"`
	Route        string    `json:"route"`
	ResourceType string    `json:"resource_type,omitempty"`
	ResourceID   string    `json:"resource_id,omitempty"`
	ClientIP     string    `json:"client_ip,omitempty"`
	StatusCode   int       `json:"status_code"`
	Outcome      string    `json:"outcome"`
	OccurredAt   time.Time `json:"occurred_at"`
}

type AuditEventInput struct {
	RequestID    string
	Actor        string
	Role         string
	Method       string
	Route        string
	ResourceType string
	ResourceID   string
	ClientIP     string
	StatusCode   int
}

type AuditFilter struct {
	Actor  string
	Limit  int
	Offset int
}

type AuditPage struct {
	Items  []AuditEvent `json:"items"`
	Limit  int          `json:"limit"`
	Offset int          `json:"offset"`
}

func (s *Service) RecordAudit(ctx context.Context, input AuditEventInput) error {
	normalized, err := normalizeAuditEvent(input)
	if err != nil {
		return err
	}
	outcome := auditOutcome(normalized.StatusCode)
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO audit_events (
	request_id, actor, role, method, route, resource_type, resource_id,
	client_ip, status_code, outcome, occurred_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		normalized.RequestID,
		normalized.Actor,
		normalized.Role,
		normalized.Method,
		normalized.Route,
		normalized.ResourceType,
		normalized.ResourceID,
		normalized.ClientIP,
		normalized.StatusCode,
		outcome,
		s.now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		return fmt.Errorf("record operation audit: %w", err)
	}
	return nil
}

func (s *Service) ListAuditEvents(ctx context.Context, filter AuditFilter) (AuditPage, error) {
	filter.Actor = strings.TrimSpace(filter.Actor)
	filter.Limit = normalizeLimit(filter.Limit, 50, 200)
	if filter.Offset < 0 {
		return AuditPage{}, errors.New("audit offset must not be negative")
	}
	condition := ""
	args := make([]any, 0, 3)
	if filter.Actor != "" {
		condition = " WHERE actor = ?"
		args = append(args, truncate(filter.Actor, 64))
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT sequence, request_id, actor, role, method, route, resource_type,
	resource_id, client_ip, status_code, outcome, occurred_at
FROM audit_events`+condition+`
ORDER BY sequence DESC
LIMIT ? OFFSET ?`,
		append(args, filter.Limit, filter.Offset)...,
	)
	if err != nil {
		return AuditPage{}, fmt.Errorf("list operation audit: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := make([]AuditEvent, 0, filter.Limit)
	for rows.Next() {
		var (
			item       AuditEvent
			occurredAt string
		)
		if err := rows.Scan(
			&item.Sequence,
			&item.RequestID,
			&item.Actor,
			&item.Role,
			&item.Method,
			&item.Route,
			&item.ResourceType,
			&item.ResourceID,
			&item.ClientIP,
			&item.StatusCode,
			&item.Outcome,
			&occurredAt,
		); err != nil {
			return AuditPage{}, fmt.Errorf("scan operation audit: %w", err)
		}
		item.OccurredAt, err = time.Parse(time.RFC3339Nano, occurredAt)
		if err != nil {
			return AuditPage{}, fmt.Errorf("parse operation audit time: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return AuditPage{}, fmt.Errorf("iterate operation audit: %w", err)
	}
	return AuditPage{Items: items, Limit: filter.Limit, Offset: filter.Offset}, nil
}

func normalizeAuditEvent(input AuditEventInput) (AuditEventInput, error) {
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.Actor = strings.TrimSpace(input.Actor)
	input.Role = strings.TrimSpace(input.Role)
	input.Method = strings.ToUpper(strings.TrimSpace(input.Method))
	input.Route = strings.TrimSpace(input.Route)
	input.ResourceType = strings.TrimSpace(input.ResourceType)
	input.ResourceID = strings.TrimSpace(input.ResourceID)
	input.ClientIP = strings.TrimSpace(input.ClientIP)
	if input.RequestID == "" || utf8.RuneCountInString(input.RequestID) > 64 {
		return AuditEventInput{}, errors.New("audit request ID is required")
	}
	if input.Actor == "" || utf8.RuneCountInString(input.Actor) > 64 {
		return AuditEventInput{}, errors.New("audit actor is required")
	}
	if input.Role == "" || utf8.RuneCountInString(input.Role) > 32 {
		return AuditEventInput{}, errors.New("audit role is required")
	}
	if input.Method == "" || utf8.RuneCountInString(input.Method) > 16 {
		return AuditEventInput{}, errors.New("audit method is required")
	}
	if input.Route == "" || utf8.RuneCountInString(input.Route) > 256 {
		return AuditEventInput{}, errors.New("audit route is required")
	}
	if input.StatusCode < 100 || input.StatusCode > 599 {
		return AuditEventInput{}, errors.New("audit status code is invalid")
	}
	input.ResourceType = truncate(input.ResourceType, 32)
	input.ResourceID = truncate(input.ResourceID, 64)
	input.ClientIP = truncate(input.ClientIP, 64)
	return input, nil
}

func auditOutcome(statusCode int) string {
	switch {
	case statusCode >= 200 && statusCode < 400:
		return "success"
	case statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden:
		return "denied"
	default:
		return "failure"
	}
}
