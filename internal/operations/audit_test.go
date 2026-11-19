package operations

import (
	"context"
	"testing"
	"time"
)

func TestAuditEventsAreRecordedAndAppendOnly(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)
	now := time.Date(2026, 11, 20, 15, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }

	input := AuditEventInput{
		RequestID:    "request-1",
		Actor:        "admin",
		Role:         "administrator",
		Method:       "GET",
		Route:        "/api/v1/accounts/{id}/chats",
		ResourceType: "account",
		ResourceID:   "42",
		ClientIP:     "127.0.0.1",
		StatusCode:   200,
	}
	if err := service.RecordAudit(ctx, input); err != nil {
		t.Fatalf("RecordAudit() error = %v", err)
	}
	page, err := service.ListAuditEvents(ctx, AuditFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListAuditEvents() error = %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("ListAuditEvents() = %+v", page)
	}
	item := page.Items[0]
	if item.RequestID != input.RequestID || item.Actor != "admin" ||
		item.Outcome != "success" || item.ResourceID != "42" {
		t.Fatalf("audit event = %+v", item)
	}
	if !item.OccurredAt.Equal(now) {
		t.Fatalf("OccurredAt = %s, want %s", item.OccurredAt, now)
	}

	if _, err := service.db.ExecContext(
		ctx,
		`UPDATE audit_events SET actor = 'other' WHERE sequence = ?`,
		item.Sequence,
	); err == nil {
		t.Fatal("audit event update unexpectedly succeeded")
	}
	if _, err := service.db.ExecContext(
		ctx,
		`DELETE FROM audit_events WHERE sequence = ?`,
		item.Sequence,
	); err == nil {
		t.Fatal("audit event deletion unexpectedly succeeded")
	}
}

func TestAuditEventRequiresIdentityAndClassifiesDeniedRequests(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)
	if err := service.RecordAudit(ctx, AuditEventInput{
		RequestID:  "request-denied",
		Actor:      "admin",
		Role:       "administrator",
		Method:     "POST",
		Route:      "/api/v1/accounts",
		StatusCode: 403,
	}); err != nil {
		t.Fatalf("RecordAudit() error = %v", err)
	}
	page, err := service.ListAuditEvents(ctx, AuditFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListAuditEvents() error = %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Outcome != "denied" {
		t.Fatalf("audit events = %+v", page.Items)
	}
	if err := service.RecordAudit(ctx, AuditEventInput{
		RequestID:  "missing-actor",
		Role:       "administrator",
		Method:     "POST",
		Route:      "/api/v1/accounts",
		StatusCode: 200,
	}); err == nil {
		t.Fatal("RecordAudit() accepted a missing actor")
	}
}
