package domain

import (
	"time"

	"github.com/google/uuid"
)

// JobKind is what a background job does. Every Claude call runs as a job.
type JobKind string

const (
	JobDraft       JobKind = "draft"        // make next week's draft
	JobSwap        JobKind = "swap"         // replace one draft meal
	JobAsk         JobKind = "ask"          // Ask Claude: change the whole draft
	JobAutoConfirm JobKind = "auto_confirm" // swap thumbs-down meals, then confirm
)

type JobStatus string

const (
	JobQueued  JobStatus = "queued"
	JobRunning JobStatus = "running"
	JobDone    JobStatus = "done"
	JobFailed  JobStatus = "failed"
)

// Pending reports whether the job has not finished yet.
func (s JobStatus) Pending() bool { return s == JobQueued || s == JobRunning }

// Job is a background job as the UI sees it.
type Job struct {
	ID        uuid.UUID  `json:"id"`
	Kind      JobKind    `json:"kind"`
	Status    JobStatus  `json:"status"`
	Error     string     `json:"error,omitempty"`
	PlanID    *uuid.UUID `json:"plan_id,omitempty"`
	MealID    *uuid.UUID `json:"meal_id,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// DraftState is what the draft card on the Week page shows.
type DraftState string

const (
	DraftNone   DraftState = "none"   // no draft and nothing being made
	DraftMaking DraftState = "making" // "Making your plan…"
	DraftReady  DraftState = "ready"  // "Next week is ready"
	DraftFailed DraftState = "failed" // "Couldn't make the plan. Try again"
)

// DraftCard is the Week page's card about next week's plan.
type DraftCard struct {
	State     DraftState `json:"state"`
	PlanID    *uuid.UUID `json:"plan_id,omitempty"`
	MealCount int        `json:"meal_count"`
	// ConfirmsAt is when the draft confirms itself (shopping day at the
	// household's auto-confirm time).
	ConfirmsAt time.Time `json:"confirms_at"`
	Error      string    `json:"error,omitempty"`
}

// Vote is one member's thumbs up (+1) or down (-1) on a draft meal.
type Vote struct {
	MemberID uuid.UUID `json:"member_id"`
	Name     string    `json:"name"`
	Vote     int       `json:"vote"`
}

// DraftMeal is a meal of the draft with its votes and swap state.
type DraftMeal struct {
	Meal
	Votes []Vote `json:"votes"`
	// Mine is the caller's vote: +1, -1 or 0.
	Mine int `json:"mine"`
	// Swapping is true while a swap job for the meal runs; SwapError is the
	// last swap's error, cleared by the next one.
	Swapping  bool   `json:"swapping"`
	SwapError string `json:"swap_error,omitempty"`
}

// Downs counts thumbs-down votes.
func (m DraftMeal) Downs() int {
	n := 0
	for _, v := range m.Votes {
		if v.Vote < 0 {
			n++
		}
	}
	return n
}

// Review is the draft as the review page shows it.
type Review struct {
	Plan       Plan        `json:"plan"`
	Meals      []DraftMeal `json:"meals"`
	ConfirmsAt time.Time   `json:"confirms_at"`
	// Asking is true while an Ask Claude job runs; AskError is the last
	// one's error.
	Asking   bool   `json:"asking"`
	AskError string `json:"ask_error,omitempty"`
}
