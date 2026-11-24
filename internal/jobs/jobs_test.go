package jobs_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Zagoman/meal-planner/internal/db"
	"github.com/Zagoman/meal-planner/internal/domain"
	"github.com/Zagoman/meal-planner/internal/jobs"
	"github.com/Zagoman/meal-planner/internal/testutil"
)

func TestDedupeAndFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewDB(t)
	sc := testutil.NewHousehold(t, pool, "A")
	q := db.New(pool)
	n := jobs.New{HouseholdID: sc.HouseholdID, Kind: domain.JobDraft, DedupeKey: "draft:2026-11-28"}
	if _, err := jobs.Enqueue(ctx, q, n); err != nil {
		t.Fatal(err)
	}
	if _, err := jobs.Enqueue(ctx, q, n); !errors.Is(err, jobs.ErrDuplicate) {
		t.Fatalf("second enqueue: %v", err)
	}
	// Another household may use the same key.
	other := testutil.NewHousehold(t, pool, "B")
	n.HouseholdID = other.HouseholdID
	if _, err := jobs.Enqueue(ctx, q, n); err != nil {
		t.Fatal(err)
	}

	w := jobs.NewWorker(pool, jobs.Options{})
	w.Handle(domain.JobDraft, func(context.Context, db.Job) (any, error) { return nil, errors.New("claude said no") })
	if err := w.RunPending(ctx); err != nil {
		t.Fatal(err)
	}
	j, err := q.LatestJob(ctx, db.LatestJobParams{HouseholdID: sc.HouseholdID, Kind: "draft"})
	if err != nil || j.Status != "failed" || j.Error != "claude said no" || j.FinishedAt == nil {
		t.Errorf("failed job: %+v %v", j, err)
	}
}

func TestEachJobRunsOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewDB(t)
	sc := testutil.NewHousehold(t, pool, "A")
	q := db.New(pool)
	const total = 20
	for range total {
		if _, err := jobs.Enqueue(ctx, q, jobs.New{HouseholdID: sc.HouseholdID, Kind: domain.JobSwap}); err != nil {
			t.Fatal(err)
		}
	}
	var runs atomic.Int32
	w := jobs.NewWorker(pool, jobs.Options{})
	w.Handle(domain.JobSwap, func(context.Context, db.Job) (any, error) {
		runs.Add(1)
		return map[string]bool{"ok": true}, nil
	})
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := w.RunPending(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if runs.Load() != total {
		t.Errorf("%d runs for %d jobs", runs.Load(), total)
	}
}

func TestStaleJobsAreRetried(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewDB(t)
	sc := testutil.NewHousehold(t, pool, "A")
	q := db.New(pool)
	job, err := jobs.Enqueue(ctx, q, jobs.New{HouseholdID: sc.HouseholdID, Kind: domain.JobAsk})
	if err != nil {
		t.Fatal(err)
	}
	// A crash left it running an hour ago.
	if _, err := pool.Exec(ctx, `UPDATE jobs SET status = 'running', attempts = 1, started_at = now() - interval '1 hour' WHERE id = $1`, job.ID); err != nil {
		t.Fatal(err)
	}
	ran := make(chan struct{}, 1)
	w := jobs.NewWorker(pool, jobs.Options{Workers: 1})
	w.Handle(domain.JobAsk, func(context.Context, db.Job) (any, error) {
		ran <- struct{}{}
		return nil, nil
	})
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { w.Run(runCtx); close(done) }()
	<-ran
	cancel()
	<-done
	j, _ := q.GetJob(ctx, db.GetJobParams{ID: job.ID, HouseholdID: sc.HouseholdID})
	if j.Status != "done" || j.Attempts != 2 {
		t.Errorf("recovered job: %+v", j)
	}
}

// A job cut off by a shutdown goes back in the queue instead of failing.
func TestShutdownRequeues(t *testing.T) {
	t.Parallel()
	pool := testutil.NewDB(t)
	sc := testutil.NewHousehold(t, pool, "A")
	q := db.New(pool)
	job, err := jobs.Enqueue(context.Background(), q, jobs.New{HouseholdID: sc.HouseholdID, Kind: domain.JobDraft})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := jobs.NewWorker(pool, jobs.Options{})
	w.Handle(domain.JobDraft, func(jctx context.Context, _ db.Job) (any, error) {
		cancel() // the server shuts down while Claude works
		<-jctx.Done()
		return nil, jctx.Err()
	})
	if _, err := w.RunOne(ctx); err != nil {
		t.Fatal(err)
	}
	j, _ := q.GetJob(context.Background(), db.GetJobParams{ID: job.ID, HouseholdID: sc.HouseholdID})
	if j.Status != "queued" || j.Error != "" {
		t.Errorf("after shutdown: %s %q", j.Status, j.Error)
	}
}
