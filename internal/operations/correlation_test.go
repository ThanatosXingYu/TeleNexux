package operations

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestAnalyzeCorrelationsRequiresMultipleAccountsAndExcludesDeletedMessages(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	ingestCorrelationMessage := func(
		accountID int64,
		messageID int,
		text string,
	) {
		t.Helper()
		if _, err := service.IngestBatch(ctx, IngestBatchInput{
			BatchID:   fmt.Sprintf("correlation-%d-%d", accountID, messageID),
			Source:    "test",
			AccountID: accountID,
			PeerType:  "chat",
			PeerID:    int64(100 + accountID),
			PeerTitle: "Operations",
			Messages: []MessageRecord{{
				AccountID: accountID,
				PeerType:  "chat",
				PeerID:    int64(100 + accountID),
				PeerTitle: "Operations",
				MessageID: messageID,
				Sender:    " Alice Smith ",
				Text:      text,
				SentAt:    now.Add(-time.Duration(messageID) * time.Minute),
			}},
		}); err != nil {
			t.Fatalf("IngestBatch(account %d) error = %v", accountID, err)
		}
	}
	sharedText := "跨账号重复的项目更新 https://Example.com/report?utm_source=test&id=7"
	ingestCorrelationMessage(1, 101, sharedText)
	ingestCorrelationMessage(2, 201, sharedText)
	ingestCorrelationMessage(3, 301, sharedText)
	ingestCorrelationMessage(1, 102, sharedText)

	analysis, err := service.AnalyzeCorrelations(ctx, CorrelationQuery{Days: 30, Limit: 20})
	if err != nil {
		t.Fatalf("AnalyzeCorrelations() error = %v", err)
	}
	if analysis.MessageCount != 4 || analysis.TotalGroups != 4 ||
		analysis.GoverningNote == "" || analysis.Truncated {
		t.Fatalf("correlation analysis = %+v", analysis)
	}
	accountCounts := make(map[string]int)
	for _, group := range analysis.Groups {
		accountCounts[group.Type] = group.AccountCount
		if len(group.Sources) == 0 || group.Sources[0].EvidenceID == "" ||
			group.Sources[0].ContentSHA256 == "" {
			t.Fatalf("correlation group lacks evidence source: %+v", group)
		}
		if group.Type == CorrelationLink &&
			!strings.Contains(group.Match, "id=7") ||
			group.Type == CorrelationLink && strings.Contains(group.Match, "utm_") {
			t.Fatalf("normalized link = %q", group.Match)
		}
	}
	for _, kind := range []string{CorrelationLink, CorrelationDomain, CorrelationSender, CorrelationContent} {
		if accountCounts[kind] != 3 {
			t.Fatalf("correlation %s account count = %d, groups = %+v",
				kind, accountCounts[kind], analysis.Groups)
		}
	}

	if _, err := service.RecordDeletion(ctx, 3, "chat", 103, []int{301}, "tester"); err != nil {
		t.Fatalf("RecordDeletion() error = %v", err)
	}
	filtered, err := service.AnalyzeCorrelations(ctx, CorrelationQuery{
		Days:  30,
		Types: []string{CorrelationLink},
		Limit: 20,
	})
	if err != nil {
		t.Fatalf("AnalyzeCorrelations(link) error = %v", err)
	}
	if len(filtered.Groups) != 1 ||
		filtered.Groups[0].Type != CorrelationLink ||
		filtered.Groups[0].AccountCount != 2 ||
		filtered.Groups[0].SourceCount != 3 {
		t.Fatalf("filtered correlation analysis = %+v", filtered)
	}
}

func TestAnalyzeCorrelationsRejectsUnknownType(t *testing.T) {
	service := newTestService(t)
	if _, err := service.AnalyzeCorrelations(context.Background(), CorrelationQuery{
		Types: []string{"identity"},
	}); err == nil {
		t.Fatal("AnalyzeCorrelations() accepted unknown type")
	}
}
