package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"synergy/internal/domain"
)

func TestFetchRunSuccessLifecycle(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	src := mustCreateSource(t, st, "hn", domain.SourceTypeHackerNews)

	run, err := st.StartFetchRun(ctx, src.ID, domain.TriggerCLI)
	if err != nil {
		t.Fatalf("StartFetchRun: %v", err)
	}
	if run.Status != domain.RunRunning || run.FinishedAt != nil || run.Trigger != domain.TriggerCLI || run.Duration() != 0 {
		t.Errorf("started run = %+v", run)
	}
	src, _ = st.GetSource(ctx, src.ID)
	if src.LastFetchAt == nil || !src.LastFetchAt.Equal(run.StartedAt) {
		t.Errorf("LastFetchAt = %v, want run start %v", src.LastFetchAt, run.StartedAt)
	}

	stats := domain.FetchStats{Fetched: 10, Inserted: 6, Updated: 1, Unchanged: 3, Duplicate: 2}
	done, err := st.FinishFetchRun(ctx, run.ID, domain.RunCompletion{
		Stats: stats,
		State: json.RawMessage(`{"last_seen":"2026-10-01"}`),
	})
	if err != nil {
		t.Fatalf("FinishFetchRun: %v", err)
	}
	if done.Status != domain.RunSucceeded || done.FinishedAt == nil || done.Stats != stats || done.Error != "" {
		t.Errorf("finished run = %+v", done)
	}
	if got, _ := st.GetFetchRun(ctx, run.ID); got.Stats != stats || got.Status != domain.RunSucceeded {
		t.Errorf("GetFetchRun = %+v", got)
	}

	src, _ = st.GetSource(ctx, src.ID)
	if src.Health() != domain.HealthHealthy || src.LastSuccessAt == nil || !src.LastSuccessAt.Equal(*done.FinishedAt) {
		t.Errorf("source after success: health=%s last_success=%v", src.Health(), src.LastSuccessAt)
	}
	if string(src.State) != `{"last_seen": "2026-10-01"}` {
		t.Errorf("State = %s, want adapter state persisted", src.State)
	}
}

func TestOneRunningFetchPerSource(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	a := mustCreateSource(t, st, "a", domain.SourceTypeHackerNews)
	b := mustCreateSource(t, st, "b", domain.SourceTypeArxiv)

	run, err := st.StartFetchRun(ctx, a.ID, domain.TriggerAPI)
	if err != nil {
		t.Fatalf("StartFetchRun: %v", err)
	}
	if _, err := st.StartFetchRun(ctx, a.ID, domain.TriggerCLI); !errors.Is(err, domain.ErrFetchInProgress) {
		t.Fatalf("second start error = %v, want ErrFetchInProgress", err)
	}
	if _, err := st.StartFetchRun(ctx, b.ID, domain.TriggerCLI); err != nil {
		t.Errorf("other source blocked: %v", err)
	}
	if _, err := st.FinishFetchRun(ctx, run.ID, domain.RunCompletion{}); err != nil {
		t.Fatalf("FinishFetchRun: %v", err)
	}
	if _, err := st.StartFetchRun(ctx, a.ID, domain.TriggerCLI); err != nil {
		t.Errorf("start after finish: %v", err)
	}
}

func TestFetchRunFailureTracksHealth(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	src := mustCreateSource(t, st, "gh", domain.SourceTypeGitHub)

	succeed := func(state json.RawMessage) {
		t.Helper()
		run, err := st.StartFetchRun(ctx, src.ID, domain.TriggerCLI)
		if err != nil {
			t.Fatalf("StartFetchRun: %v", err)
		}
		if _, err := st.FinishFetchRun(ctx, run.ID, domain.RunCompletion{State: state}); err != nil {
			t.Fatalf("FinishFetchRun: %v", err)
		}
	}
	fail := func(msg string) domain.FetchRun {
		t.Helper()
		run, err := st.StartFetchRun(ctx, src.ID, domain.TriggerCLI)
		if err != nil {
			t.Fatalf("StartFetchRun: %v", err)
		}
		// State passed with a failure must be ignored.
		done, err := st.FinishFetchRun(ctx, run.ID, domain.RunCompletion{
			Err: msg, Stats: domain.FetchStats{Fetched: 1}, State: json.RawMessage(`{"cursor":"bad"}`),
		})
		if err != nil {
			t.Fatalf("FinishFetchRun(failure): %v", err)
		}
		return done
	}

	succeed(json.RawMessage(`{"cursor":"good"}`))

	wantHealth := []domain.Health{domain.HealthDegraded, domain.HealthDegraded, domain.HealthFailing}
	for i, want := range wantHealth {
		done := fail("github: 503 Service Unavailable")
		if done.Status != domain.RunFailed || done.Error == "" {
			t.Errorf("failed run = %+v", done)
		}
		got, _ := st.GetSource(ctx, src.ID)
		if got.ConsecutiveFailures != i+1 || got.Health() != want {
			t.Errorf("after %d failures: count=%d health=%s, want %s", i+1, got.ConsecutiveFailures, got.Health(), want)
		}
		if got.LastError != "github: 503 Service Unavailable" || got.LastFailureAt == nil {
			t.Errorf("last_error=%q last_failure_at=%v", got.LastError, got.LastFailureAt)
		}
		if string(got.State) != `{"cursor": "good"}` {
			t.Errorf("state changed on failure: %s", got.State)
		}
	}

	// Recovery resets the streak; a nil state keeps the stored cursor.
	succeed(nil)
	got, _ := st.GetSource(ctx, src.ID)
	if got.Health() != domain.HealthHealthy || got.ConsecutiveFailures != 0 || got.LastError != "" {
		t.Errorf("after recovery: health=%s failures=%d last_error=%q", got.Health(), got.ConsecutiveFailures, got.LastError)
	}
	if got.LastFailureAt == nil {
		t.Error("recovery erased last_failure_at history")
	}
	if string(got.State) != `{"cursor": "good"}` {
		t.Errorf("nil state overwrote cursor: %s", got.State)
	}
}

func TestFinishFetchRunErrors(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	src := mustCreateSource(t, st, "hn", domain.SourceTypeHackerNews)
	run, _ := st.StartFetchRun(ctx, src.ID, domain.TriggerCLI)

	if _, err := st.FinishFetchRun(ctx, run.ID, domain.RunCompletion{State: json.RawMessage(`{`)}); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("invalid state error = %v, want ErrInvalid", err)
	}
	if _, err := st.FinishFetchRun(ctx, run.ID, domain.RunCompletion{State: json.RawMessage(`[1]`)}); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("non-object state error = %v, want ErrInvalid (rejected by the database)", err)
	}
	if _, err := st.FinishFetchRun(ctx, run.ID, domain.RunCompletion{}); err != nil {
		t.Fatalf("FinishFetchRun: %v", err)
	}
	if _, err := st.FinishFetchRun(ctx, run.ID, domain.RunCompletion{}); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("double finish error = %v, want ErrConflict", err)
	}
	if _, err := st.FinishFetchRun(ctx, uuid.New(), domain.RunCompletion{}); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown run error = %v, want ErrNotFound", err)
	}
	if _, err := st.GetFetchRun(ctx, uuid.New()); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("GetFetchRun error = %v, want ErrNotFound", err)
	}
}

func TestStartFetchRunErrors(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	src := mustCreateSource(t, st, "hn", domain.SourceTypeHackerNews)

	if _, err := st.StartFetchRun(ctx, uuid.New(), domain.TriggerCLI); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown source error = %v, want ErrNotFound", err)
	}
	if _, err := st.StartFetchRun(ctx, src.ID, "cron"); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("invalid trigger error = %v, want ErrInvalid", err)
	}
}

func TestFetchRunErrorIsTruncated(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	src := mustCreateSource(t, st, "hn", domain.SourceTypeHackerNews)
	run, _ := st.StartFetchRun(ctx, src.ID, domain.TriggerCLI)

	long := strings.Repeat("é", maxErrorLen) // 2 bytes per rune
	done, err := st.FinishFetchRun(ctx, run.ID, domain.RunCompletion{Err: long})
	if err != nil {
		t.Fatalf("FinishFetchRun: %v", err)
	}
	if len(done.Error) > maxErrorLen || !strings.HasPrefix(long, done.Error) {
		t.Errorf("stored error length %d, want <= %d and a valid prefix", len(done.Error), maxErrorLen)
	}
}

func TestFailAbandonedRuns(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	src := mustCreateSource(t, st, "hn", domain.SourceTypeHackerNews)
	run, _ := st.StartFetchRun(ctx, src.ID, domain.TriggerAPI)

	n, err := st.FailAbandonedRuns(ctx, time.Now().Add(-time.Hour), "abandoned")
	if err != nil || n != 0 {
		t.Errorf("old cutoff marked %d runs (%v), want 0", n, err)
	}
	n, err = st.FailAbandonedRuns(ctx, time.Now().Add(time.Minute), "abandoned: process restarted")
	if err != nil || n != 1 {
		t.Fatalf("marked %d runs (%v), want 1", n, err)
	}
	got, _ := st.GetFetchRun(ctx, run.ID)
	if got.Status != domain.RunFailed || got.Error != "abandoned: process restarted" || got.FinishedAt == nil {
		t.Errorf("abandoned run = %+v", got)
	}
	if _, err := st.StartFetchRun(ctx, src.ID, domain.TriggerCLI); err != nil {
		t.Errorf("source still blocked after abandon: %v", err)
	}
}

func TestListFetchRuns(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	src := mustCreateSource(t, st, "hn", domain.SourceTypeHackerNews)
	other := mustCreateSource(t, st, "other", domain.SourceTypeArxiv)

	var ids []uuid.UUID
	for range 3 {
		run, err := st.StartFetchRun(ctx, src.ID, domain.TriggerCLI)
		if err != nil {
			t.Fatalf("StartFetchRun: %v", err)
		}
		if _, err := st.FinishFetchRun(ctx, run.ID, domain.RunCompletion{}); err != nil {
			t.Fatalf("FinishFetchRun: %v", err)
		}
		ids = append(ids, run.ID)
	}
	if _, err := st.StartFetchRun(ctx, other.ID, domain.TriggerCLI); err != nil {
		t.Fatalf("StartFetchRun(other): %v", err)
	}

	runs, err := st.ListFetchRuns(ctx, src.ID, 2)
	if err != nil {
		t.Fatalf("ListFetchRuns: %v", err)
	}
	if len(runs) != 2 || runs[0].ID != ids[2] || runs[1].ID != ids[1] {
		t.Errorf("runs = %v, want newest two of %v", runs, ids)
	}
}
