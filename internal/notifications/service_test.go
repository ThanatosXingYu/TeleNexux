package notifications

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ThanatosXingYu/TeleNexux/internal/operations"
)

type fakeDeliveryStore struct {
	mu           sync.Mutex
	deliveries   []operations.NotificationDelivery
	completedIDs []int64
	retries      []int64
}

func (s *fakeDeliveryStore) ClaimNotificationDelivery(
	_ context.Context,
) (operations.NotificationDelivery, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.deliveries) == 0 {
		return operations.NotificationDelivery{}, false, nil
	}
	item := s.deliveries[0]
	s.deliveries = s.deliveries[1:]
	return item, true, nil
}

func (s *fakeDeliveryStore) CompleteNotificationDelivery(
	_ context.Context,
	id int64,
	_ int,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completedIDs = append(s.completedIDs, id)
	return nil
}

func (s *fakeDeliveryStore) RetryNotificationDelivery(
	_ context.Context,
	id int64,
	_ int,
	_ string,
) (string, *time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retries = append(s.retries, id)
	next := time.Now().UTC().Add(time.Minute)
	return "queued", &next, nil
}

func TestRunOnceDeliversSignedWebhook(t *testing.T) {
	var (
		mu        sync.Mutex
		signature string
		payload   webhookPayload
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read webhook body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		signature = r.Header.Get("X-TeleNexus-Signature")
		_ = json.Unmarshal(body, &payload)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	config, err := json.Marshal(operations.WebhookChannelConfig{
		URL:    server.URL,
		Secret: "signing-secret-1234",
	})
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	store := &fakeDeliveryStore{deliveries: []operations.NotificationDelivery{{
		ID:             11,
		AlertID:        12,
		ChannelID:      13,
		ChannelKind:    operations.NotificationKindWebhook,
		ChannelConfig:  string(config),
		AttemptCount:   1,
		RuleName:       "Security",
		PeerTitle:      "Ops",
		MessagePreview: "deploy now",
		Severity:       "high",
		AlertCreatedAt: time.Now().UTC(),
	}}}
	service := New(store, Options{AllowPrivate: true}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := service.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if len(store.completedIDs) != 1 || store.completedIDs[0] != 11 {
		t.Fatalf("completed IDs = %+v", store.completedIDs)
	}
	mu.Lock()
	defer mu.Unlock()
	if payload.Event != "monitor.alert" || payload.MessagePreview != "deploy now" {
		t.Fatalf("payload = %+v", payload)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	mac := hmac.New(sha256.New, []byte("signing-secret-1234"))
	_, _ = mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if signature != want {
		t.Fatalf("signature = %q, want %q", signature, want)
	}
}

func TestRunOnceQueuesRetryAfterWebhookFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	config, err := json.Marshal(operations.WebhookChannelConfig{URL: server.URL})
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	store := &fakeDeliveryStore{deliveries: []operations.NotificationDelivery{{
		ID:            21,
		ChannelID:     22,
		ChannelKind:   operations.NotificationKindWebhook,
		ChannelConfig: string(config),
		AttemptCount:  2,
	}}}
	service := New(store, Options{AllowPrivate: true}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := service.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if len(store.retries) != 1 || store.retries[0] != 21 {
		t.Fatalf("retries = %+v", store.retries)
	}
	if len(store.completedIDs) != 0 {
		t.Fatalf("completed IDs = %+v", store.completedIDs)
	}
}

func TestValidateDestinationRejectsPrivateNetwork(t *testing.T) {
	service := New(&fakeDeliveryStore{}, Options{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := service.validateDestination(context.Background(), "https://127.0.0.1/hook"); err == nil {
		t.Fatal("validateDestination() accepted a loopback address")
	}
	if err := service.validateDestination(context.Background(), "http://example.com/hook"); err == nil {
		t.Fatal("validateDestination() accepted plain HTTP")
	}
}
