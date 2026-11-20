package accountpurge

import (
	"context"
	"errors"
	"testing"

	"github.com/ThanatosXingYu/TeleNexux/internal/account"
	"github.com/ThanatosXingYu/TeleNexux/internal/operations"
)

type fakeLifecycle struct {
	due        []account.Account
	claimed    bool
	failed     bool
	finalized  bool
	claimError error
}

func (f *fakeLifecycle) ListDueForPurge(
	context.Context,
	int,
) ([]account.Account, error) {
	return f.due, nil
}

func (f *fakeLifecycle) ClaimPurge(
	_ context.Context,
	id int64,
) (account.Account, error) {
	if f.claimError != nil {
		return account.Account{}, f.claimError
	}
	f.claimed = true
	return account.Account{ID: id, PurgeState: account.PurgeStatePurging}, nil
}

func (f *fakeLifecycle) FailPurge(context.Context, int64, string) error {
	f.failed = true
	return nil
}

func (f *fakeLifecycle) FinalizePurge(context.Context, int64) error {
	f.finalized = true
	return nil
}

type fakeSessions struct {
	deleted bool
	err     error
}

func (f *fakeSessions) DeleteSession(int64) error {
	f.deleted = true
	return f.err
}

type fakeEvidence struct {
	checkErr error
	purged   bool
}

type fakeProxyCleaner struct {
	deleted bool
	err     error
}

func (f *fakeProxyCleaner) Delete(context.Context, int64) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	f.deleted = true
	return true, nil
}

func (f *fakeEvidence) CheckAccountPurge(context.Context, int64) error {
	return f.checkErr
}

func (f *fakeEvidence) PurgeAccount(context.Context, int64) error {
	f.purged = true
	return nil
}

func TestRunOncePurgesClaimedAccount(t *testing.T) {
	lifecycle := &fakeLifecycle{
		due: []account.Account{{ID: 7, PurgeState: account.PurgeStateScheduled}},
	}
	sessions := &fakeSessions{}
	evidence := &fakeEvidence{}
	service, err := New(lifecycle, sessions, evidence)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := service.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if !lifecycle.claimed || !lifecycle.finalized ||
		!sessions.deleted || !evidence.purged {
		t.Fatalf("purge steps = lifecycle %+v sessions %+v evidence %+v",
			lifecycle, sessions, evidence)
	}
}

func TestRunOnceSkipsAccountWithActiveLegalHold(t *testing.T) {
	lifecycle := &fakeLifecycle{
		due: []account.Account{{ID: 7, PurgeState: account.PurgeStateScheduled}},
	}
	sessions := &fakeSessions{}
	evidence := &fakeEvidence{checkErr: operations.ErrActiveLegalHold}
	service, err := New(lifecycle, sessions, evidence)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := service.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if lifecycle.claimed || sessions.deleted || evidence.purged {
		t.Fatal("held account data was modified")
	}
}

func TestRunOnceRecordsCompensationFailure(t *testing.T) {
	lifecycle := &fakeLifecycle{
		due: []account.Account{{ID: 7, PurgeState: account.PurgeStateScheduled}},
	}
	service, err := New(
		lifecycle,
		&fakeSessions{err: errors.New("unsafe session path")},
		&fakeEvidence{},
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	err = service.RunOnce(context.Background())
	if err == nil || !lifecycle.failed {
		t.Fatalf("RunOnce() error = %v, failed = %v", err, lifecycle.failed)
	}
}

func TestRunOnceRetriesFailedClaims(t *testing.T) {
	lifecycle := &fakeLifecycle{claimError: account.ErrDeletionInProgress}
	service, err := New(lifecycle, nil, &fakeEvidence{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := service.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if lifecycle.failed {
		t.Fatal("competing claim was treated as a purge failure")
	}
}

func TestRunOnceDeletesProxyCredentialsBeforeAccountRecord(t *testing.T) {
	lifecycle := &fakeLifecycle{
		due: []account.Account{{ID: 7, PurgeState: account.PurgeStateScheduled}},
	}
	proxies := &fakeProxyCleaner{}
	service, err := New(
		lifecycle,
		&fakeSessions{},
		&fakeEvidence{},
		WithCredentialCleaner(proxies),
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := service.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if !proxies.deleted {
		t.Fatal("proxy credentials were not deleted")
	}
}

func TestRunOnceRetainsAccountWhenProxyCleanupFails(t *testing.T) {
	lifecycle := &fakeLifecycle{
		due: []account.Account{{ID: 7, PurgeState: account.PurgeStateScheduled}},
	}
	service, err := New(
		lifecycle,
		&fakeSessions{},
		&fakeEvidence{},
		WithCredentialCleaner(&fakeProxyCleaner{err: errors.New("storage unavailable")}),
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	err = service.RunOnce(context.Background())
	if err == nil || !lifecycle.failed || lifecycle.finalized {
		t.Fatalf("RunOnce() error = %v, failed = %t, finalized = %t",
			err, lifecycle.failed, lifecycle.finalized)
	}
}
