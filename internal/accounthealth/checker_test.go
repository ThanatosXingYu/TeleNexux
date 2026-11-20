package accounthealth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/ThanatosXingYu/TeleNexux/internal/account"
	"github.com/ThanatosXingYu/TeleNexux/internal/telegramchat"
)

type fakeAccountStore struct {
	mu            sync.Mutex
	items         []account.Account
	verifications map[int64]string
	reasons       map[int64]string
	authorized    map[int64]account.VerificationInput
	recordErr     error
}

func (f *fakeAccountStore) List(
	_ context.Context,
	filter account.Filter,
) ([]account.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var items []account.Account
	for _, item := range f.items {
		if item.Status != filter.Status {
			continue
		}
		items = append(items, item)
	}
	if filter.Offset >= len(items) {
		return nil, nil
	}
	items = items[filter.Offset:]
	if len(items) > filter.Limit {
		items = items[:filter.Limit]
	}
	return items, nil
}

func (f *fakeAccountStore) RecordVerification(
	_ context.Context,
	id int64,
	status string,
	reason string,
) (account.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recordErr != nil {
		return account.Account{}, f.recordErr
	}
	f.verifications[id] = status
	f.reasons[id] = reason
	return account.Account{ID: id, Status: status}, nil
}

func (f *fakeAccountStore) RecordAuthorized(
	_ context.Context,
	input account.VerificationInput,
) (account.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recordErr != nil {
		return account.Account{}, f.recordErr
	}
	f.authorized[input.ID] = input
	return account.Account{ID: input.ID, Status: account.StatusOnline}, nil
}

type fakeValidator struct {
	mu       sync.Mutex
	profiles map[int64]telegramchat.Profile
	errors   map[int64]error
	seen     map[int64]bool
}

func (f *fakeValidator) InspectSession(
	_ context.Context,
	accountID int64,
) (telegramchat.Profile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen[accountID] = true
	return f.profiles[accountID], f.errors[accountID]
}

func TestRunOncePersistsOnlyDefinitiveSessionResults(t *testing.T) {
	store := &fakeAccountStore{
		items: []account.Account{
			{ID: 1, Status: account.StatusOnline},
			{ID: 2, Status: account.StatusOnline},
			{ID: 3, Status: account.StatusError},
			{ID: 4, Status: account.StatusPaused},
		},
		verifications: make(map[int64]string),
		reasons:       make(map[int64]string),
		authorized:    make(map[int64]account.VerificationInput),
	}
	validator := &fakeValidator{
		profiles: map[int64]telegramchat.Profile{
			1: {ID: 1, PhoneNumber: "+15550000001", Username: "fresh", DisplayName: "Fresh"},
		},
		errors: map[int64]error{
			2: telegramchat.ErrSessionInvalid,
			3: telegramchat.ErrSessionMissing,
		},
		seen: make(map[int64]bool),
	}
	checker := New(store, validator)
	summary, err := checker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if summary.Checked != 3 || summary.Online != 1 ||
		summary.Offline != 2 || summary.Deferred != 0 {
		t.Fatalf("RunOnce() summary = %+v", summary)
	}
	if store.authorized[1].DisplayName != "Fresh" ||
		store.authorized[1].PhoneNumber != "+15550000001" {
		t.Fatalf("authorized account = %+v, want fresh profile", store.authorized[1])
	}
	if store.verifications[2] != account.StatusError ||
		store.verifications[3] != account.StatusError {
		t.Fatalf("verifications = %+v", store.verifications)
	}
	if validator.seen[4] {
		t.Fatal("paused account was validated")
	}
}

func TestRunOnceDefersTransientFailures(t *testing.T) {
	store := &fakeAccountStore{
		items:         []account.Account{{ID: 7, Status: account.StatusOnline}},
		verifications: make(map[int64]string),
		reasons:       make(map[int64]string),
		authorized:    make(map[int64]account.VerificationInput),
	}
	validator := &fakeValidator{
		profiles: map[int64]telegramchat.Profile{},
		errors: map[int64]error{
			7: fmt.Errorf("%w: temporary network failure", telegramchat.ErrTelegram),
		},
		seen: make(map[int64]bool),
	}
	summary, err := New(store, validator).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if summary.Deferred != 1 || len(store.verifications) != 0 {
		t.Fatalf("summary = %+v, verifications = %+v", summary, store.verifications)
	}
}

func TestRunOnceRejectsMismatchedTelegramProfile(t *testing.T) {
	store := &fakeAccountStore{
		items:         []account.Account{{ID: 8, Status: account.StatusOnline}},
		verifications: make(map[int64]string),
		reasons:       make(map[int64]string),
		authorized:    make(map[int64]account.VerificationInput),
	}
	validator := &fakeValidator{
		profiles: map[int64]telegramchat.Profile{
			8: {ID: 99, PhoneNumber: "+15550000099", DisplayName: "Wrong account"},
		},
		errors: map[int64]error{},
		seen:   make(map[int64]bool),
	}
	summary, err := New(store, validator).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if summary.Offline != 1 ||
		store.verifications[8] != account.StatusError ||
		!strings.Contains(store.reasons[8], "账号不一致") ||
		store.authorized[8].ID != 0 {
		t.Fatalf("summary = %+v, verifications = %+v, authorized = %+v",
			summary, store.verifications, store.authorized)
	}
}

func TestRunOnceReportsPersistenceFailure(t *testing.T) {
	store := &fakeAccountStore{
		items:         []account.Account{{ID: 9, Status: account.StatusOnline}},
		verifications: make(map[int64]string),
		reasons:       make(map[int64]string),
		authorized:    make(map[int64]account.VerificationInput),
		recordErr:     errors.New("database unavailable"),
	}
	summary, err := New(store, &fakeValidator{
		profiles: map[int64]telegramchat.Profile{9: {ID: 9, PhoneNumber: "+15550000009"}},
		errors:   map[int64]error{},
		seen:     make(map[int64]bool),
	}).RunOnce(context.Background())
	if !errors.Is(err, store.recordErr) {
		t.Fatalf("RunOnce() error = %v, want persistence failure", err)
	}
	if summary.Failed != 1 {
		t.Fatalf("summary = %+v", summary)
	}
}
