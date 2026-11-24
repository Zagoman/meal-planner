// Package jobs is the background job queue: a table (jobs) and a small
// worker inside the server binary. Every Claude call runs as a job, so no
// page waits on Claude.
//
// Jobs are claimed with SELECT … FOR UPDATE SKIP LOCKED, so several workers
// (or two servers during a deploy) never run the same job. A handler must be
// idempotent: a job left running by a crash is run again (up to three
// attempts), and scheduled jobs carry a dedupe key so a schedule fires once.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Zagoman/meal-planner/internal/db"
	"github.com/Zagoman/meal-planner/internal/domain"
)

// Handler runs one job. The returned value is stored as the job's result
// (JSON). An error marks the job failed with err.Error() as its message.
type Handler func(ctx context.Context, job db.Job) (any, error)

// New is a job to queue.
type New struct {
	HouseholdID uuid.UUID
	Kind        domain.JobKind
	DedupeKey   string // "" for none
	PlanID      uuid.UUID
	MealID      uuid.UUID
	MemberID    uuid.UUID
	Payload     any
	RunAfter    time.Time // zero for now
}

// ErrDuplicate is returned by Enqueue when the household already has a job
// with the same dedupe key.
var ErrDuplicate = errors.New("job already queued")

// Enqueue inserts a job using q (a pool or a transaction).
func Enqueue(ctx context.Context, q *db.Queries, n New) (db.Job, error) {
	payload := []byte("{}")
	if n.Payload != nil {
		var err error
		if payload, err = json.Marshal(n.Payload); err != nil {
			return db.Job{}, fmt.Errorf("job payload: %w", err)
		}
	}
	var runAfter *time.Time
	if !n.RunAfter.IsZero() {
		runAfter = &n.RunAfter
	}
	job, err := q.InsertJob(ctx, db.InsertJobParams{
		HouseholdID: n.HouseholdID,
		Kind:        string(n.Kind),
		DedupeKey:   pgtype.Text{String: n.DedupeKey, Valid: n.DedupeKey != ""},
		PlanID:      nullUUID(n.PlanID),
		MealID:      nullUUID(n.MealID),
		MemberID:    nullUUID(n.MemberID),
		Payload:     payload,
		RunAfter:    runAfter,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Job{}, ErrDuplicate
	}
	if err != nil {
		return db.Job{}, fmt.Errorf("insert job: %w", err)
	}
	return job, nil
}

func nullUUID(id uuid.UUID) uuid.NullUUID { return uuid.NullUUID{UUID: id, Valid: id != uuid.Nil} }

// FromDB converts a stored job for the UI.
func FromDB(j db.Job) domain.Job {
	out := domain.Job{
		ID: j.ID, Kind: domain.JobKind(j.Kind), Status: domain.JobStatus(j.Status),
		Error: j.Error, CreatedAt: j.CreatedAt,
	}
	if j.PlanID.Valid {
		id := j.PlanID.UUID
		out.PlanID = &id
	}
	if j.MealID.Valid {
		id := j.MealID.UUID
		out.MealID = &id
	}
	return out
}

// Options tune a Worker; zero values get defaults.
type Options struct {
	Workers int           // goroutines claiming jobs (2)
	Poll    time.Duration // how often an idle worker looks for due jobs (2 s)
	Timeout time.Duration // per job (90 s)
	// StaleAfter: a job running longer than this is considered abandoned
	// by a crashed process and is retried (10 min).
	StaleAfter time.Duration
	Logger     *slog.Logger
}

// Worker runs queued jobs.
type Worker struct {
	pool     *pgxpool.Pool
	handlers map[domain.JobKind]Handler
	opts     Options
	wake     chan struct{}
	mu       sync.Mutex
}

// NewWorker returns a worker; register handlers with Handle before Run.
func NewWorker(pool *pgxpool.Pool, opts Options) *Worker {
	if opts.Workers <= 0 {
		opts.Workers = 2
	}
	if opts.Poll <= 0 {
		opts.Poll = 2 * time.Second
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 90 * time.Second
	}
	if opts.StaleAfter <= 0 {
		opts.StaleAfter = 10 * time.Minute
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Worker{pool: pool, handlers: map[domain.JobKind]Handler{}, opts: opts, wake: make(chan struct{}, 1)}
}

// Handle registers the handler for a kind.
func (w *Worker) Handle(kind domain.JobKind, h Handler) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.handlers[kind] = h
}

// Wake makes an idle worker look for jobs now instead of at the next poll.
func (w *Worker) Wake() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Run processes jobs until ctx is done.
func (w *Worker) Run(ctx context.Context) {
	if n, err := db.New(w.pool).RecoverStaleJobs(ctx, time.Now().Add(-w.opts.StaleAfter)); err != nil {
		w.opts.Logger.Error("recover stale jobs", "err", err)
	} else if n > 0 {
		w.opts.Logger.Warn("recovered stale jobs", "count", n)
	}
	var wg sync.WaitGroup
	for range w.opts.Workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.loop(ctx)
		}()
	}
	wg.Wait()
}

func (w *Worker) loop(ctx context.Context) {
	t := time.NewTicker(w.opts.Poll)
	defer t.Stop()
	for {
		for {
			ran, err := w.RunOne(ctx)
			if err != nil && ctx.Err() == nil {
				w.opts.Logger.Error("job queue", "err", err)
			}
			if !ran || ctx.Err() != nil {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-w.wake:
			w.Wake() // let the other workers look too
		}
	}
}

// RunPending runs every due job in this goroutine, for tests.
func (w *Worker) RunPending(ctx context.Context) error {
	for {
		ran, err := w.RunOne(ctx)
		if err != nil || !ran {
			return err
		}
	}
}

// RunOne claims and runs one due job. It reports whether there was one.
func (w *Worker) RunOne(ctx context.Context) (bool, error) {
	q := db.New(w.pool)
	job, err := q.ClaimJob(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim job: %w", err)
	}
	log := w.opts.Logger.With("job_id", job.ID, "kind", job.Kind, "household_id", job.HouseholdID)
	w.mu.Lock()
	h := w.handlers[domain.JobKind(job.Kind)]
	w.mu.Unlock()

	var result any
	if h == nil {
		err = fmt.Errorf("no handler for job kind %q", job.Kind)
	} else {
		result, err = w.run(ctx, h, job)
	}
	// The outcome is stored even if ctx was cancelled meanwhile.
	done := context.WithoutCancel(ctx)
	if err != nil && ctx.Err() != nil && job.Attempts < 3 {
		// Stopped by a shutdown (a deploy), not by the job: run it again
		// after the restart instead of failing it.
		log.Info("job interrupted by shutdown, requeued")
		if rerr := q.RequeueJob(done, job.ID); rerr != nil {
			return true, fmt.Errorf("requeue job: %w", rerr)
		}
		return true, nil
	}
	if err != nil {
		log.Warn("job failed", "attempt", job.Attempts, "err", err)
		if ferr := q.FailJob(done, db.FailJobParams{ID: job.ID, Error: truncate(err.Error(), 1000)}); ferr != nil {
			return true, fmt.Errorf("mark job failed: %w", ferr)
		}
		return true, nil
	}
	raw, merr := json.Marshal(result)
	if merr != nil || result == nil {
		raw = nil
	}
	if ferr := q.FinishJob(done, db.FinishJobParams{ID: job.ID, Result: raw}); ferr != nil {
		return true, fmt.Errorf("mark job done: %w", ferr)
	}
	log.Info("job done", "ms", time.Since(*job.StartedAt).Milliseconds())
	return true, nil
}

// run calls the handler with the job timeout and turns a panic into an error.
func (w *Worker) run(ctx context.Context, h Handler, job db.Job) (result any, err error) {
	ctx, cancel := context.WithTimeout(ctx, w.opts.Timeout)
	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("job panicked: %v", r)
		}
	}()
	return h(ctx, job)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
