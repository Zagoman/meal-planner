package planner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Zagoman/meal-planner/internal/db"
	"github.com/Zagoman/meal-planner/internal/domain"
	"github.com/Zagoman/meal-planner/internal/events"
	"github.com/Zagoman/meal-planner/internal/jobs"
	"github.com/Zagoman/meal-planner/internal/mail"
	"github.com/Zagoman/meal-planner/internal/service"
	"github.com/Zagoman/meal-planner/internal/shopping"
)

var _ service.Drafts = (*Service)(nil)

// Usage kinds for the window 2 jobs (the draft itself logs kindPlan).
const (
	kindAsk         = "ask"
	kindAutoConfirm = "auto_confirm"
)

// draftPayload is a draft job's payload.
type draftPayload struct {
	WeekStart string `json:"week_start"` // 2006-01-02, the shopping day
	// Replace: a newer draft replaces one for the same week. Scheduled
	// drafts do not, and skip a week that already has a draft or plan.
	Replace bool `json:"replace"`
}

type askPayload struct {
	Prompt string `json:"prompt"`
}

type autoConfirmPayload struct {
	WeekStart string `json:"week_start"`
}

// RegisterJobs adds the planner's job handlers to w.
func (s *Service) RegisterJobs(w *jobs.Worker) {
	w.Handle(domain.JobDraft, s.failing(s.runDraft))
	w.Handle(domain.JobSwap, s.failing(s.runSwap))
	w.Handle(domain.JobAsk, s.failing(s.runAsk))
	w.Handle(domain.JobAutoConfirm, s.failing(s.runAutoConfirm))
	w.Handle(domain.JobTweak, s.failing(s.runTweak))
	w.Handle(domain.JobImport, s.failing(s.runImport))
}

// failing publishes a job's failure so the pending element on every phone
// shows its error state.
func (s *Service) failing(h jobs.Handler) jobs.Handler {
	return func(ctx context.Context, job db.Job) (any, error) {
		res, err := h(ctx, job)
		if err != nil && s.events != nil {
			e := events.Event{Kind: events.JobFailed}
			if job.MealID.Valid {
				e.MealID = job.MealID.UUID
				s.events.Publish(job.HouseholdID, events.Event{Kind: events.MealUpdated, MealID: job.MealID.UUID})
			}
			s.events.Publish(job.HouseholdID, e)
			s.events.Publish(job.HouseholdID, events.Event{Kind: events.PlanUpdated})
		}
		return res, err
	}
}

func (s *Service) publishEvent(householdID uuid.UUID, e events.Event) {
	if s.events != nil {
		s.events.Publish(householdID, e)
	}
}

func (s *Service) wake() {
	if s.opts.Wake != nil {
		s.opts.Wake()
	}
}

// ---- schedule helpers ------------------------------------------------------

// location is the household's time zone, Europe/Copenhagen if unknown.
func location(p domain.Profile) *time.Location {
	for _, name := range []string{p.TimeZone, "Europe/Copenhagen"} {
		if name == "" {
			continue
		}
		if loc, err := time.LoadLocation(name); err == nil {
			return loc
		}
	}
	return time.UTC
}

// dateOf is the calendar date of t (in t's zone) as stored in a date column.
func dateOf(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func goWeekday(d domain.Weekday) time.Weekday {
	for i, w := range []domain.Weekday{domain.Sunday, domain.Monday, domain.Tuesday, domain.Wednesday,
		domain.Thursday, domain.Friday, domain.Saturday} {
		if w == d {
			return time.Weekday(i)
		}
	}
	return time.Saturday
}

// nextShoppingDate is the shopping day on or after local's date.
func nextShoppingDate(local time.Time, day domain.Weekday) time.Time {
	d := dateOf(local)
	offset := (int(goWeekday(day)) - int(d.Weekday()) + 7) % 7
	return d.AddDate(0, 0, offset)
}

// covers reports whether a confirmed plan already is the plan for the
// shopping week starting on week. Plans from the review flow start on the
// shopping day; older plans start on a Monday and only mark when they were
// made, so they never cover a shopping week.
func covers(active db.Plan, week time.Time) bool {
	return active.WeekStart.Weekday() == week.Weekday() && !active.WeekStart.Before(week)
}

// confirmsAt is when a draft for weekStart confirms itself: that date at
// the auto-confirm time, in the household's zone.
func confirmsAt(weekStart time.Time, p domain.Profile) time.Time {
	loc := location(p)
	h, m := 8, 0
	_, _ = fmt.Sscanf(p.AutoConfirmTime, "%d:%d", &h, &m)
	return time.Date(weekStart.Year(), weekStart.Month(), weekStart.Day(), h, m, 0, 0, loc)
}

// ---- reads -------------------------------------------------------------------

func (s *Service) profile(ctx context.Context, q *db.Queries, householdID uuid.UUID) (domain.Profile, error) {
	pr, err := q.GetProfile(ctx, householdID)
	if err != nil {
		return domain.Profile{}, notFound("profile", err)
	}
	return ProfileFromDB(pr), nil
}

// Card is the Week page's draft card.
func (s *Service) Card(ctx context.Context, sc domain.Scope) (domain.DraftCard, error) {
	q := db.New(s.pool)
	card := domain.DraftCard{State: domain.DraftNone}
	job, err := q.LatestJob(ctx, db.LatestJobParams{HouseholdID: sc.HouseholdID, Kind: string(domain.JobDraft)})
	hasJob := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return card, fmt.Errorf("latest draft job: %w", err)
	}
	if hasJob && domain.JobStatus(job.Status).Pending() {
		card.State = domain.DraftMaking
		return card, nil
	}
	draft, err := q.GetDraftPlan(ctx, sc.HouseholdID)
	switch {
	case err == nil:
		p, err := s.profile(ctx, q, sc.HouseholdID)
		if err != nil {
			return card, err
		}
		n, err := q.CountMeals(ctx, db.CountMealsParams{PlanID: draft.ID, HouseholdID: sc.HouseholdID})
		if err != nil {
			return card, fmt.Errorf("count meals: %w", err)
		}
		id := draft.ID
		card.State, card.PlanID, card.MealCount = domain.DraftReady, &id, int(n)
		card.ConfirmsAt = confirmsAt(draft.WeekStart, p)
		if card.RateFirst, err = s.unrated(ctx, q, sc.HouseholdID); err != nil {
			return card, err
		}
		return card, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return card, fmt.Errorf("draft plan: %w", err)
	}
	// No draft: show a failure until a plan is confirmed after it.
	if hasJob && job.Status == string(domain.JobFailed) {
		active, err := q.GetActivePlan(ctx, sc.HouseholdID)
		if err != nil || active.ConfirmedAt == nil || job.CreatedAt.After(*active.ConfirmedAt) {
			card.State, card.Error = domain.DraftFailed, job.Error
		}
	}
	return card, nil
}

// Review returns the newest draft with votes and job states.
func (s *Service) Review(ctx context.Context, sc domain.Scope) (domain.Review, error) {
	q := db.New(s.pool)
	draft, err := q.GetDraftPlan(ctx, sc.HouseholdID)
	if err != nil {
		return domain.Review{}, notFound("draft", err)
	}
	plan, err := s.loadPlan(ctx, q, sc.HouseholdID, draft.ID)
	if err != nil {
		return domain.Review{}, err
	}
	p, err := s.profile(ctx, q, sc.HouseholdID)
	if err != nil {
		return domain.Review{}, err
	}
	votes, err := q.ListVotesForPlan(ctx, db.ListVotesForPlanParams{PlanID: draft.ID, HouseholdID: sc.HouseholdID})
	if err != nil {
		return domain.Review{}, fmt.Errorf("votes: %w", err)
	}
	r := domain.Review{Plan: plan, ConfirmsAt: confirmsAt(draft.WeekStart, p), Meals: make([]domain.DraftMeal, 0, len(plan.Meals))}
	for _, m := range plan.Meals {
		dm := domain.DraftMeal{Meal: m, Votes: []domain.Vote{}}
		for _, v := range votes {
			if v.MealID == m.ID {
				dm.Votes = append(dm.Votes, voteOf(v.MemberID, v.Name, v.Email, v.Vote))
				if v.MemberID == sc.MemberID {
					dm.Mine = int(v.Vote)
				}
			}
		}
		if err := s.swapState(ctx, q, sc.HouseholdID, &dm); err != nil {
			return domain.Review{}, err
		}
		r.Meals = append(r.Meals, dm)
	}
	r.NotCooked = []domain.NotCookedMeal{}
	if active, err := q.GetActivePlan(ctx, sc.HouseholdID); err == nil {
		rows, err := q.ListNotCooked(ctx, db.ListNotCookedParams{PlanID: active.ID, HouseholdID: sc.HouseholdID, OtherPlanID: draft.ID})
		if err != nil {
			return domain.Review{}, fmt.Errorf("not cooked: %w", err)
		}
		for _, m := range rows {
			r.NotCooked = append(r.NotCooked, domain.NotCookedMeal{RecipeID: m.RecipeID.UUID, Title: m.Title, MealType: domain.MealType(m.MealType)})
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return domain.Review{}, fmt.Errorf("active plan: %w", err)
	}
	ask, err := q.LatestJob(ctx, db.LatestJobParams{HouseholdID: sc.HouseholdID, Kind: string(domain.JobAsk)})
	if err == nil && ask.PlanID.Valid && ask.PlanID.UUID == draft.ID {
		r.Asking = domain.JobStatus(ask.Status).Pending()
		if ask.Status == string(domain.JobFailed) {
			r.AskError = ask.Error
		}
	} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return domain.Review{}, fmt.Errorf("ask job: %w", err)
	}
	return r, nil
}

func voteOf(memberID uuid.UUID, name, email string, v int16) domain.Vote {
	if name == "" {
		name, _, _ = strings.Cut(email, "@")
	}
	return domain.Vote{MemberID: memberID, Name: name, Vote: int(v)}
}

func (s *Service) swapState(ctx context.Context, q *db.Queries, householdID uuid.UUID, dm *domain.DraftMeal) error {
	job, err := q.LatestMealJob(ctx, db.LatestMealJobParams{HouseholdID: householdID, MealID: uuid.NullUUID{UUID: dm.ID, Valid: true}})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("swap job: %w", err)
	}
	dm.Swapping = domain.JobStatus(job.Status).Pending()
	if job.Status == string(domain.JobFailed) {
		dm.SwapError = job.Error
	}
	return nil
}

// draftMeal loads a meal that must belong to a draft plan.
func (s *Service) draftMeal(ctx context.Context, q *db.Queries, householdID, mealID uuid.UUID) (db.Meal, db.Plan, error) {
	m, err := q.GetMeal(ctx, db.GetMealParams{ID: mealID, HouseholdID: householdID})
	if err != nil {
		return db.Meal{}, db.Plan{}, notFound("meal", err)
	}
	p, err := q.GetPlan(ctx, db.GetPlanParams{ID: m.PlanID, HouseholdID: householdID})
	if err != nil {
		return db.Meal{}, db.Plan{}, notFound("plan", err)
	}
	if p.Status != string(domain.PlanDraft) {
		return db.Meal{}, db.Plan{}, fmt.Errorf("the meal's plan is already confirmed: %w", domain.ErrConflict)
	}
	return m, p, nil
}

// DraftMeal returns one draft meal with its votes.
func (s *Service) DraftMeal(ctx context.Context, sc domain.Scope, mealID uuid.UUID) (domain.DraftMeal, error) {
	q := db.New(s.pool)
	m, err := q.GetMeal(ctx, db.GetMealParams{ID: mealID, HouseholdID: sc.HouseholdID})
	if err != nil {
		return domain.DraftMeal{}, notFound("meal", err)
	}
	meal, err := loadMeal(ctx, q, m)
	if err != nil {
		return domain.DraftMeal{}, err
	}
	dm := domain.DraftMeal{Meal: meal, Votes: []domain.Vote{}}
	votes, err := q.ListVotesForMeal(ctx, db.ListVotesForMealParams{MealID: mealID, HouseholdID: sc.HouseholdID})
	if err != nil {
		return domain.DraftMeal{}, fmt.Errorf("votes: %w", err)
	}
	for _, v := range votes {
		dm.Votes = append(dm.Votes, voteOf(v.MemberID, v.Name, v.Email, v.Vote))
		if v.MemberID == sc.MemberID {
			dm.Mine = int(v.Vote)
		}
	}
	return dm, s.swapState(ctx, q, sc.HouseholdID, &dm)
}

// ListPreview is the list the draft would make; nothing is stored.
func (s *Service) ListPreview(ctx context.Context, sc domain.Scope) (domain.List, error) {
	q := db.New(s.pool)
	draft, err := q.GetDraftPlan(ctx, sc.HouseholdID)
	if err != nil {
		return domain.List{}, notFound("draft", err)
	}
	ings, err := q.ListIngredientsForPlan(ctx, db.ListIngredientsForPlanParams{PlanID: draft.ID, HouseholdID: sc.HouseholdID})
	if err != nil {
		return domain.List{}, fmt.Errorf("ingredients: %w", err)
	}
	secRows, err := q.ListSections(ctx, sc.HouseholdID)
	if err != nil {
		return domain.List{}, fmt.Errorf("sections: %w", err)
	}
	pantryRows, err := q.ListPantryItems(ctx, sc.HouseholdID)
	if err != nil {
		return domain.List{}, fmt.Errorf("pantry: %w", err)
	}
	sections := make([]domain.Section, len(secRows))
	for i, r := range secRows {
		sections[i] = domain.Section{Key: r.Key, Name: r.Name, SortOrder: int(r.SortOrder)}
	}
	pantry := make([]string, len(pantryRows))
	for i, r := range pantryRows {
		pantry[i] = r.NormalizedName
	}
	domIngs := make([]domain.Ingredient, len(ings))
	for i, in := range ings {
		domIngs[i] = shopping.IngredientFromDB(in)
	}
	built := shopping.Build(domIngs, sections, pantry)
	items := make([]domain.ListItem, len(built))
	for i, it := range built {
		items[i] = domain.ListItem{
			PlanID: draft.ID, Name: it.Name, QtyText: it.QtyText, NeedText: it.NeedText,
			Section: it.Section, CheckHome: it.CheckHome,
		}
	}
	return shopping.Group(draft.ID, items, sections), nil
}

// ---- writes ------------------------------------------------------------------

// RequestDraft queues a draft for the next shopping day. A draft job that
// is already queued or running is returned instead of a second one.
func (s *Service) RequestDraft(ctx context.Context, sc domain.Scope) (domain.Job, error) {
	if s.client == nil {
		return domain.Job{}, errNoClient
	}
	q := db.New(s.pool)
	if job, ok, err := pendingJob(ctx, q, sc.HouseholdID, domain.JobDraft); err != nil || ok {
		return job, err
	}
	p, err := s.profile(ctx, q, sc.HouseholdID)
	if err != nil {
		return domain.Job{}, err
	}
	if p.TotalMeals() == 0 {
		return domain.Job{}, &domain.ValidationError{Problems: []string{"the profile plans no meals; set at least one meal per week"}}
	}
	week := nextShoppingDate(s.opts.Now().In(location(p)), p.ShoppingDay)
	// A week that already has a confirmed plan gets the following week.
	if active, err := q.GetActivePlan(ctx, sc.HouseholdID); err == nil && covers(active, week) {
		week = week.AddDate(0, 0, 7)
	}
	job, err := jobs.Enqueue(ctx, q, jobs.New{
		HouseholdID: sc.HouseholdID, Kind: domain.JobDraft, MemberID: sc.MemberID,
		Payload: draftPayload{WeekStart: week.Format(time.DateOnly), Replace: true},
	})
	if err != nil {
		return domain.Job{}, err
	}
	s.publishEvent(sc.HouseholdID, events.Event{Kind: events.PlanUpdated})
	s.wake()
	return jobs.FromDB(job), nil
}

func pendingJob(ctx context.Context, q *db.Queries, householdID uuid.UUID, kind domain.JobKind) (domain.Job, bool, error) {
	pending, err := q.ListPendingJobs(ctx, householdID)
	if err != nil {
		return domain.Job{}, false, fmt.Errorf("pending jobs: %w", err)
	}
	for _, j := range pending {
		if j.Kind == string(kind) {
			return jobs.FromDB(j), true, nil
		}
	}
	return domain.Job{}, false, nil
}

// Vote sets or clears the caller's vote on a draft meal.
func (s *Service) Vote(ctx context.Context, sc domain.Scope, mealID uuid.UUID, vote int) (domain.DraftMeal, error) {
	if vote < -1 || vote > 1 {
		return domain.DraftMeal{}, &domain.ValidationError{Problems: []string{"vote: 1, -1 or 0"}}
	}
	if sc.MemberID == uuid.Nil {
		return domain.DraftMeal{}, &domain.ValidationError{Problems: []string{"vote: only a member can vote"}}
	}
	q := db.New(s.pool)
	if _, _, err := s.draftMeal(ctx, q, sc.HouseholdID, mealID); err != nil {
		return domain.DraftMeal{}, err
	}
	var err error
	if vote == 0 {
		err = q.DeleteVote(ctx, db.DeleteVoteParams{MealID: mealID, MemberID: sc.MemberID, HouseholdID: sc.HouseholdID})
	} else {
		err = q.UpsertVote(ctx, db.UpsertVoteParams{HouseholdID: sc.HouseholdID, MealID: mealID, MemberID: sc.MemberID, Vote: int16(vote)}) //nolint:gosec // -1 or 1
	}
	if err != nil {
		return domain.DraftMeal{}, fmt.Errorf("vote: %w", err)
	}
	s.publishEvent(sc.HouseholdID, events.Event{Kind: events.MealUpdated, MealID: mealID})
	return s.DraftMeal(ctx, sc, mealID)
}

// RequestSwap queues a swap job for one draft meal.
func (s *Service) RequestSwap(ctx context.Context, sc domain.Scope, mealID uuid.UUID) (domain.DraftMeal, error) {
	q := db.New(s.pool)
	m, p, err := s.draftMeal(ctx, q, sc.HouseholdID, mealID)
	if err != nil {
		return domain.DraftMeal{}, err
	}
	if s.client == nil {
		return domain.DraftMeal{}, errNoClient
	}
	dm, err := s.DraftMeal(ctx, sc, mealID)
	if err != nil || dm.Swapping {
		return dm, err
	}
	if _, err := jobs.Enqueue(ctx, q, jobs.New{
		HouseholdID: sc.HouseholdID, Kind: domain.JobSwap, PlanID: p.ID, MealID: m.ID, MemberID: sc.MemberID,
	}); err != nil {
		return domain.DraftMeal{}, err
	}
	s.publishEvent(sc.HouseholdID, events.Event{Kind: events.MealUpdated, MealID: mealID})
	s.wake()
	return s.DraftMeal(ctx, sc, mealID)
}

// RemoveMeal deletes a meal from the draft.
func (s *Service) RemoveMeal(ctx context.Context, sc domain.Scope, mealID uuid.UUID) error {
	q := db.New(s.pool)
	if _, _, err := s.draftMeal(ctx, q, sc.HouseholdID, mealID); err != nil {
		return err
	}
	if err := q.DeleteMeal(ctx, db.DeleteMealParams{ID: mealID, HouseholdID: sc.HouseholdID}); err != nil {
		return fmt.Errorf("delete meal: %w", err)
	}
	s.publishEvent(sc.HouseholdID, events.Event{Kind: events.MealUpdated, MealID: mealID})
	return nil
}

// RequestAsk queues an Ask Claude job for the draft.
func (s *Service) RequestAsk(ctx context.Context, sc domain.Scope, prompt string) (domain.Job, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return domain.Job{}, &domain.ValidationError{Problems: []string{"prompt: say what to change"}}
	}
	if utf8.RuneCountInString(prompt) > 500 {
		return domain.Job{}, &domain.ValidationError{Problems: []string{"prompt: at most 500 characters"}}
	}
	q := db.New(s.pool)
	draft, err := q.GetDraftPlan(ctx, sc.HouseholdID)
	if err != nil {
		return domain.Job{}, notFound("draft", err)
	}
	if s.client == nil {
		return domain.Job{}, errNoClient
	}
	if job, ok, err := pendingJob(ctx, q, sc.HouseholdID, domain.JobAsk); err != nil || ok {
		return job, err
	}
	job, err := jobs.Enqueue(ctx, q, jobs.New{
		HouseholdID: sc.HouseholdID, Kind: domain.JobAsk, PlanID: draft.ID, MemberID: sc.MemberID,
		Payload: askPayload{Prompt: prompt},
	})
	if err != nil {
		return domain.Job{}, err
	}
	s.publishEvent(sc.HouseholdID, events.Event{Kind: events.PlanUpdated})
	s.wake()
	return jobs.FromDB(job), nil
}

// Confirm makes the newest draft the active plan. Without a draft, the
// active plan is returned, so a second tap from another phone succeeds too.
func (s *Service) Confirm(ctx context.Context, sc domain.Scope) (domain.Plan, error) {
	q := db.New(s.pool)
	draft, err := q.GetDraftPlan(ctx, sc.HouseholdID)
	if errors.Is(err, pgx.ErrNoRows) {
		return s.Current(ctx, sc)
	}
	if err != nil {
		return domain.Plan{}, fmt.Errorf("draft: %w", err)
	}
	if err := s.confirm(ctx, sc.HouseholdID, draft.ID, memberRef(sc), false, ""); err != nil {
		return domain.Plan{}, err
	}
	return s.loadPlan(ctx, q, sc.HouseholdID, draft.ID)
}

// confirm archives the active plan, activates the draft and builds its
// list, in one transaction. The draft row is locked, so two confirms at once
// confirm once. A draft for the week that is already under way (or for the
// same week as the active plan) keeps manual items and the ticks of items
// still needed.
func (s *Service) confirm(ctx context.Context, householdID, planID uuid.UUID, by uuid.NullUUID, auto bool, note string) error {
	q := db.New(s.pool)
	p, err := s.profile(ctx, q, householdID)
	if err != nil {
		return err
	}
	today := dateOf(s.opts.Now().In(location(p)))
	confirmed := false
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		qt := q.WithTx(tx)
		draft, err := qt.LockPlan(ctx, db.LockPlanParams{ID: planID, HouseholdID: householdID})
		if err != nil {
			return notFound("plan", err)
		}
		switch draft.Status {
		case string(domain.PlanActive):
			return nil // already confirmed
		case string(domain.PlanDraft):
		default:
			return fmt.Errorf("confirm: the plan is archived: %w", domain.ErrConflict)
		}
		old, oerr := qt.GetActivePlan(ctx, householdID)
		if oerr != nil && !errors.Is(oerr, pgx.ErrNoRows) {
			return fmt.Errorf("active plan: %w", oerr)
		}
		hasOld := oerr == nil
		carry := hasOld && (!draft.WeekStart.After(today) || draft.WeekStart.Equal(old.WeekStart))
		if err := qt.ArchiveActivePlan(ctx, householdID); err != nil {
			return fmt.Errorf("archive: %w", err)
		}
		n, err := qt.ConfirmDraftPlan(ctx, db.ConfirmDraftPlanParams{
			ConfirmedBy: by, AutoConfirmed: auto, ConfirmNote: note, ID: planID, HouseholdID: householdID,
		})
		if err != nil {
			return fmt.Errorf("confirm: %w", err)
		}
		if n != 1 {
			return fmt.Errorf("confirm: %w", domain.ErrConflict)
		}
		if err := shopping.RebuildItems(ctx, qt, householdID, planID); err != nil {
			return err
		}
		if carry {
			if err := carryOver(ctx, qt, householdID, old.ID, planID); err != nil {
				return err
			}
		}
		confirmed = true
		return nil
	})
	if err != nil {
		return err
	}
	if confirmed {
		s.publishEvent(householdID, events.Event{Kind: events.ListRebuilt})
		s.publishEvent(householdID, events.Event{Kind: events.PlanUpdated})
	}
	return nil
}

// carryOver copies manual items and the ticks and Have it answers of items
// still needed from the old plan's list to the new one.
func carryOver(ctx context.Context, q *db.Queries, householdID, oldPlan, newPlan uuid.UUID) error {
	oldItems, err := q.ListItemsForPlan(ctx, db.ListItemsForPlanParams{PlanID: oldPlan, HouseholdID: householdID})
	if err != nil {
		return fmt.Errorf("old list: %w", err)
	}
	newItems, err := q.ListItemsForPlan(ctx, db.ListItemsForPlanParams{PlanID: newPlan, HouseholdID: householdID})
	if err != nil {
		return fmt.Errorf("new list: %w", err)
	}
	byName := map[string]db.ListItem{}
	for _, it := range newItems {
		if !it.IsManual {
			byName[it.NormalizedName] = it
		}
	}
	for _, old := range oldItems {
		target := uuid.Nil
		if old.IsManual {
			added, err := q.AddManualListItem(ctx, db.AddManualListItemParams{
				HouseholdID: householdID, PlanID: newPlan, Name: old.Name, NormalizedName: old.NormalizedName,
				Section: old.Section, AddedBy: old.AddedBy,
			})
			if err != nil {
				return fmt.Errorf("carry manual item: %w", err)
			}
			target = added.ID
		} else if it, ok := byName[old.NormalizedName]; ok {
			target = it.ID
			if it.CheckHome && old.CheckHome && old.HaveIt.Valid {
				if _, err := q.SetListItemHaveIt(ctx, db.SetListItemHaveItParams{HaveIt: old.HaveIt, ID: it.ID, HouseholdID: householdID}); err != nil {
					return fmt.Errorf("carry have it: %w", err)
				}
			}
		}
		if target != uuid.Nil && old.Checked {
			if _, err := q.SetListItemChecked(ctx, db.SetListItemCheckedParams{
				Checked: true, CheckedBy: old.CheckedBy, ID: target, HouseholdID: householdID,
			}); err != nil {
				return fmt.Errorf("carry tick: %w", err)
			}
		}
	}
	return nil
}

// ---- job handlers ------------------------------------------------------------

func jobScope(job db.Job) domain.Scope {
	return domain.Scope{HouseholdID: job.HouseholdID, MemberID: job.MemberID.UUID}
}

func (s *Service) needClient() error {
	if s.client == nil {
		return errNoClient
	}
	return nil
}

// runDraft makes a draft for the payload's week.
func (s *Service) runDraft(ctx context.Context, job db.Job) (any, error) {
	if err := s.needClient(); err != nil {
		return nil, err
	}
	var p draftPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return nil, fmt.Errorf("draft payload: %w", err)
	}
	week, err := time.Parse(time.DateOnly, p.WeekStart)
	if err != nil {
		return nil, fmt.Errorf("draft payload: %w", err)
	}
	sc := jobScope(job)
	q := db.New(s.pool)
	skip := func() (bool, error) {
		if p.Replace {
			return false, nil
		}
		if d, err := q.GetDraftPlanForWeek(ctx, db.GetDraftPlanForWeekParams{HouseholdID: sc.HouseholdID, WeekStart: week}); err == nil {
			// A draft holding only meals added by hand is filled in.
			n, err := q.CountClaudeMeals(ctx, db.CountClaudeMealsParams{PlanID: d.ID, HouseholdID: sc.HouseholdID})
			return n > 0, err
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return false, err
		}
		active, err := q.GetActivePlan(ctx, sc.HouseholdID)
		if err == nil && covers(active, week) {
			return true, nil
		}
		return false, nil
	}
	if done, err := skip(); err != nil || done {
		return map[string]any{"skipped": done}, err
	}
	hh, err := s.loadHousehold(ctx, q, sc.HouseholdID)
	if err != nil {
		return nil, err
	}
	if hh.profile.TotalMeals() == 0 {
		return nil, fmt.Errorf("%w: the profile plans no meals", domain.ErrGeneration)
	}
	// Meals the household added to a draft for this week by hand (from the
	// library, or added back) stay in the new one.
	kept, err := s.handAdded(ctx, q, sc.HouseholdID, week)
	if err != nil {
		return nil, err
	}
	in := PromptInput{Profile: hh.profile, Sections: hh.sections, Feedback: hh.feedback, Banned: hh.bannedLines()}
	if len(kept) > 0 {
		for _, k := range kept {
			in.Request += fmt.Sprintf("The household already added %q (%s); do not plan it again. ", k.Title, k.MealType)
		}
	}
	mp, err := s.ask(ctx, sc, BuildSystem(hh.profile), BuildPlanPrompt(in), hh.rules(nil), kindPlan)
	if err != nil {
		return nil, err
	}
	var draft db.Plan
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		qt := q.WithTx(tx)
		if err := qt.DeleteDraftForWeek(ctx, db.DeleteDraftForWeekParams{HouseholdID: sc.HouseholdID, WeekStart: week}); err != nil {
			return fmt.Errorf("replace draft: %w", err)
		}
		var err error
		draft, err = qt.CreateDraftPlan(ctx, db.CreateDraftPlanParams{
			HouseholdID: sc.HouseholdID, WeekStart: week, CreatedBy: job.MemberID,
		})
		if err != nil {
			return fmt.Errorf("create draft: %w", err)
		}
		for i, m := range mp.Meals {
			if _, err := insertMeal(ctx, qt, sc.HouseholdID, draft.ID, i, m); err != nil {
				return err
			}
		}
		for i, k := range kept {
			if _, err := insertRecipeMeal(ctx, qt, sc.HouseholdID, draft.ID, len(mp.Meals)+i, k.recipe, k.from); err != nil {
				return err
			}
		}
		return nil
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && !p.Replace {
		return map[string]any{"skipped": true}, nil // another job made it meanwhile
	}
	if err != nil {
		return nil, fmt.Errorf("%w: save draft: %w", domain.ErrGeneration, err)
	}
	s.publishEvent(sc.HouseholdID, events.Event{Kind: events.PlanUpdated})
	// Email everyone for the weekly draft (a scheduled job) and for a
	// household's very first plan; not when someone tapped "make it now".
	if job.DedupeKey.Valid || s.firstPlan(ctx, sc.HouseholdID) {
		s.mailDraftReady(context.WithoutCancel(ctx), q, sc.HouseholdID, draft, len(mp.Meals)+len(kept))
	}
	return map[string]any{"plan_id": draft.ID}, nil
}

// firstPlan reports whether the household has no plan besides its draft.
func (s *Service) firstPlan(ctx context.Context, householdID uuid.UUID) bool {
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM plans WHERE household_id = $1 AND status <> 'draft'`, householdID).Scan(&n); err != nil {
		return false
	}
	return n == 0
}

// mailDraftReady emails every member that next week's plan is ready.
// Failures are logged; the draft is there either way.
func (s *Service) mailDraftReady(ctx context.Context, q *db.Queries, householdID uuid.UUID, draft db.Plan, meals int) {
	if s.opts.Mailer == nil {
		return
	}
	p, err := s.profile(ctx, q, householdID)
	if err != nil {
		s.opts.Logger.Error("draft email: profile", "household_id", householdID, "err", err)
		return
	}
	members, err := q.ListMemberEmails(ctx, householdID)
	if err != nil {
		s.opts.Logger.Error("draft email: members", "household_id", householdID, "err", err)
		return
	}
	at := confirmsAt(draft.WeekStart, p)
	_, week := draft.WeekStart.ISOWeek()
	subject := fmt.Sprintf("Your plan for week %d is ready.", week)
	intro := fmt.Sprintf("%d %s, ready for you to look over together. It confirms itself on %s at %s.",
		meals, plural(meals, "meal", "meals"), at.Format("Monday"), at.Format("15:04"))
	for _, m := range members {
		msg := mail.Simple(m.Email, subject, intro, "Review plan",
			strings.TrimRight(s.opts.BaseURL, "/")+"/plans/next", "")
		if err := s.opts.Mailer.Send(ctx, msg); err != nil {
			s.opts.Logger.Error("draft email", "household_id", householdID, "err", err)
		}
	}
}

// runSwap replaces one draft meal in place.
func (s *Service) runSwap(ctx context.Context, job db.Job) (any, error) {
	if err := s.needClient(); err != nil {
		return nil, err
	}
	sc := jobScope(job)
	q := db.New(s.pool)
	old, plan, err := s.draftMeal(ctx, q, sc.HouseholdID, job.MealID.UUID)
	if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrConflict) {
		return map[string]any{"skipped": true}, nil // removed or confirmed meanwhile
	}
	if err != nil {
		return nil, err
	}
	meals, err := q.ListMeals(ctx, db.ListMealsParams{PlanID: plan.ID, HouseholdID: sc.HouseholdID})
	if err != nil {
		return nil, fmt.Errorf("list meals: %w", err)
	}
	var others []string
	for _, m := range meals {
		if m.ID != old.ID {
			others = append(others, m.Title)
		}
	}
	hh, err := s.loadHousehold(ctx, q, sc.HouseholdID)
	if err != nil {
		return nil, err
	}
	day := domain.Weekday(old.Day.String)
	in := SwapInput{
		PromptInput: PromptInput{Profile: hh.profile, Sections: hh.sections, Feedback: hh.feedback, Banned: hh.bannedLines()},
		MealType:    domain.MealType(old.MealType), Day: day, Current: old.Title, OtherMeals: others,
	}
	mp, err := s.ask(ctx, sc, BuildSystem(hh.profile), BuildSwapPrompt(in),
		hh.rules(&SwapRule{MealType: domain.MealType(old.MealType), Day: day}), kindSwap)
	if err != nil {
		return nil, err
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		qt := q.WithTx(tx)
		locked, err := qt.LockPlan(ctx, db.LockPlanParams{ID: plan.ID, HouseholdID: sc.HouseholdID})
		if err != nil || locked.Status != string(domain.PlanDraft) {
			return fmt.Errorf("swap: the draft was confirmed meanwhile: %w", domain.ErrConflict)
		}
		return replaceMeal(ctx, qt, sc.HouseholdID, old.ID, mp.Meals[0])
	})
	if err != nil {
		return nil, err
	}
	s.publishEvent(sc.HouseholdID, events.Event{Kind: events.MealUpdated, MealID: old.ID})
	return map[string]any{"title": mp.Meals[0].Title}, nil
}

func mealKey(t, title string) string { return t + "|" + shopping.Normalize(title) }

// runAsk applies the household's request to the whole draft: meals Claude
// keeps (same type and title) keep their id and votes, others are removed
// and new ones added.
func (s *Service) runAsk(ctx context.Context, job db.Job) (any, error) {
	if err := s.needClient(); err != nil {
		return nil, err
	}
	var p askPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return nil, fmt.Errorf("ask payload: %w", err)
	}
	sc := jobScope(job)
	q := db.New(s.pool)
	plan, err := q.GetPlan(ctx, db.GetPlanParams{ID: job.PlanID.UUID, HouseholdID: sc.HouseholdID})
	if err != nil || plan.Status != string(domain.PlanDraft) {
		return map[string]any{"skipped": true}, nil
	}
	meals, err := q.ListMeals(ctx, db.ListMealsParams{PlanID: plan.ID, HouseholdID: sc.HouseholdID})
	if err != nil {
		return nil, fmt.Errorf("list meals: %w", err)
	}
	current := make([]MealLine, len(meals))
	for i, m := range meals {
		current[i] = MealLine{Type: domain.MealType(m.MealType), Day: domain.Weekday(m.Day.String), Title: m.Title}
	}
	hh, err := s.loadHousehold(ctx, q, sc.HouseholdID)
	if err != nil {
		return nil, err
	}
	// Meals already in the draft may stay, even a not-cooked one added back.
	for _, m := range meals {
		delete(hh.banned, domain.TitleKey(m.Title))
	}
	in := PromptInput{Profile: hh.profile, Sections: hh.sections, Feedback: hh.feedback, Request: p.Prompt, Banned: hh.bannedLines()}
	rules := hh.rules(nil)
	rules.AnyCount = true
	mp, err := s.ask(ctx, sc, BuildSystem(hh.profile), BuildAskPrompt(in, current), rules, kindAsk)
	if err != nil {
		return nil, err
	}
	kept, added, removed := 0, 0, 0
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		qt := q.WithTx(tx)
		locked, err := qt.LockPlan(ctx, db.LockPlanParams{ID: plan.ID, HouseholdID: sc.HouseholdID})
		if err != nil || locked.Status != string(domain.PlanDraft) {
			return fmt.Errorf("ask: the draft was confirmed meanwhile: %w", domain.ErrConflict)
		}
		existing, err := qt.ListMeals(ctx, db.ListMealsParams{PlanID: plan.ID, HouseholdID: sc.HouseholdID})
		if err != nil {
			return err
		}
		pool := map[string][]db.Meal{}
		for _, m := range existing {
			k := mealKey(m.MealType, m.Title)
			pool[k] = append(pool[k], m)
		}
		keep := map[uuid.UUID]bool{}
		for i, m := range mp.Meals {
			k := mealKey(string(m.MealType), m.Title)
			if list := pool[k]; len(list) > 0 {
				pool[k] = list[1:]
				keep[list[0].ID] = true
				kept++
				if err := qt.SetMealPosition(ctx, db.SetMealPositionParams{ID: list[0].ID, HouseholdID: sc.HouseholdID, Position: clamp32(i)}); err != nil {
					return err
				}
				continue
			}
			if _, err := insertMeal(ctx, qt, sc.HouseholdID, plan.ID, i, m); err != nil {
				return err
			}
			added++
		}
		for _, m := range existing {
			if !keep[m.ID] {
				if err := qt.DeleteMeal(ctx, db.DeleteMealParams{ID: m.ID, HouseholdID: sc.HouseholdID}); err != nil {
					return err
				}
				removed++
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.publishEvent(sc.HouseholdID, events.Event{Kind: events.PlanUpdated})
	return map[string]int{"kept": kept, "added": added, "removed": removed}, nil
}

// runAutoConfirm swaps every meal with a thumbs-down in one Claude call and
// confirms the draft. If that call fails, the draft is confirmed with those
// meals kept and a note says so.
func (s *Service) runAutoConfirm(ctx context.Context, job db.Job) (any, error) {
	var p autoConfirmPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return nil, fmt.Errorf("auto-confirm payload: %w", err)
	}
	week, err := time.Parse(time.DateOnly, p.WeekStart)
	if err != nil {
		return nil, fmt.Errorf("auto-confirm payload: %w", err)
	}
	sc := jobScope(job)
	q := db.New(s.pool)
	draft, err := q.GetDraftPlan(ctx, sc.HouseholdID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && draft.WeekStart.After(week)) {
		return map[string]any{"skipped": true}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("draft: %w", err)
	}
	meals, err := q.ListMeals(ctx, db.ListMealsParams{PlanID: draft.ID, HouseholdID: sc.HouseholdID})
	if err != nil {
		return nil, fmt.Errorf("list meals: %w", err)
	}
	votes, err := q.ListVotesForPlan(ctx, db.ListVotesForPlanParams{PlanID: draft.ID, HouseholdID: sc.HouseholdID})
	if err != nil {
		return nil, fmt.Errorf("votes: %w", err)
	}
	down := map[uuid.UUID]bool{}
	for _, v := range votes {
		if v.Vote < 0 {
			down[v.MealID] = true
		}
	}
	var disliked []db.Meal
	var keep []string
	for _, m := range meals {
		if down[m.ID] {
			disliked = append(disliked, m)
		} else {
			keep = append(keep, m.Title)
		}
	}
	note := ""
	swapped := 0
	if len(disliked) > 0 {
		if swapped, err = s.replaceDisliked(ctx, sc, draft.ID, disliked, keep); err != nil {
			s.opts.Logger.Warn("auto-confirm swap failed", "household_id", sc.HouseholdID, "err", err)
			note = fmt.Sprintf("Confirmed automatically. Couldn't swap %d disliked %s, so %s kept.",
				len(disliked), plural(len(disliked), "meal", "meals"), plural(len(disliked), "it was", "they were"))
		}
	}
	if err := s.confirm(ctx, sc.HouseholdID, draft.ID, uuid.NullUUID{}, true, note); err != nil {
		return nil, err
	}
	return map[string]any{"plan_id": draft.ID, "swapped": swapped, "kept_disliked": note != ""}, nil
}

func (s *Service) replaceDisliked(ctx context.Context, sc domain.Scope, planID uuid.UUID, disliked []db.Meal, keep []string) (int, error) {
	if err := s.needClient(); err != nil {
		return 0, err
	}
	q := db.New(s.pool)
	hh, err := s.loadHousehold(ctx, q, sc.HouseholdID)
	if err != nil {
		return 0, err
	}
	lines := make([]MealLine, len(disliked))
	types := make([]domain.MealType, len(disliked))
	for i, m := range disliked {
		lines[i] = MealLine{Type: domain.MealType(m.MealType), Day: domain.Weekday(m.Day.String), Title: m.Title}
		types[i] = domain.MealType(m.MealType)
	}
	in := PromptInput{Profile: hh.profile, Sections: hh.sections, Feedback: hh.feedback, Banned: hh.bannedLines()}
	rules := hh.rules(nil)
	rules.Replace = types
	mp, err := s.ask(ctx, sc, BuildSystem(hh.profile), BuildReplacePrompt(in, lines, keep), rules, kindAutoConfirm)
	if err != nil {
		return 0, err
	}
	// Pair each disliked meal with a replacement of its type, in order.
	byType := map[domain.MealType][]domain.MealJSON{}
	for _, m := range mp.Meals {
		byType[m.MealType] = append(byType[m.MealType], m)
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		qt := q.WithTx(tx)
		locked, err := qt.LockPlan(ctx, db.LockPlanParams{ID: planID, HouseholdID: sc.HouseholdID})
		if err != nil || locked.Status != string(domain.PlanDraft) {
			return fmt.Errorf("auto-confirm: %w", domain.ErrConflict)
		}
		for _, m := range disliked {
			t := domain.MealType(m.MealType)
			next := byType[t][0]
			byType[t] = byType[t][1:]
			if next.Day == nil && m.Day.Valid {
				d := domain.Weekday(m.Day.String)
				next.Day = &d
			}
			if err := replaceMeal(ctx, qt, sc.HouseholdID, m.ID, next); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return len(disliked), nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// ---- schedules -----------------------------------------------------------------

// Tick queues the scheduled jobs that are due at now: the weekly draft from
// 18:00 the evening before the shopping day, and the auto-confirm on the
// shopping day from the household's auto-confirm time. Dedupe keys make
// every schedule fire once, so Tick can run every minute.
func (s *Service) Tick(ctx context.Context, now time.Time) error {
	q := db.New(s.pool)
	rows, err := q.ListSchedules(ctx)
	if err != nil {
		return fmt.Errorf("schedules: %w", err)
	}
	queued := false
	for _, r := range rows {
		p := domain.Profile{TimeZone: r.TimeZone, ShoppingDay: domain.Weekday(r.ShoppingDay), AutoConfirmTime: ClockText(r.AutoConfirmTime)}
		local := now.In(location(p))
		shop := goWeekday(p.ShoppingDay)

		if s.client != nil && local.AddDate(0, 0, 1).Weekday() == shop && local.Hour() >= DraftHour {
			week := dateOf(local.AddDate(0, 0, 1)).Format(time.DateOnly)
			_, err := jobs.Enqueue(ctx, q, jobs.New{
				HouseholdID: r.HouseholdID, Kind: domain.JobDraft, DedupeKey: "draft:" + week,
				Payload: draftPayload{WeekStart: week},
			})
			switch {
			case err == nil:
				queued = true
				s.publishEvent(r.HouseholdID, events.Event{Kind: events.PlanUpdated})
			case !errors.Is(err, jobs.ErrDuplicate):
				return err
			}
		}

		if local.Weekday() == shop && !local.Before(confirmsAt(dateOf(local), p)) {
			week := dateOf(local)
			draft, err := q.GetDraftPlan(ctx, r.HouseholdID)
			if errors.Is(err, pgx.ErrNoRows) || (err == nil && draft.WeekStart.After(week)) {
				continue
			}
			if err != nil {
				return fmt.Errorf("draft: %w", err)
			}
			_, err = jobs.Enqueue(ctx, q, jobs.New{
				HouseholdID: r.HouseholdID, Kind: domain.JobAutoConfirm, PlanID: draft.ID,
				DedupeKey: "auto_confirm:" + week.Format(time.DateOnly),
				Payload:   autoConfirmPayload{WeekStart: week.Format(time.DateOnly)},
			})
			switch {
			case err == nil:
				queued = true
			case !errors.Is(err, jobs.ErrDuplicate):
				return err
			}
		}
	}
	if queued {
		s.wake()
	}
	return nil
}

// DraftHour is when the weekly draft is made, the evening before the
// shopping day, in the household's time zone.
const DraftHour = 18

// RunSchedules calls Tick every minute until ctx is done.
func (s *Service) RunSchedules(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		if err := s.Tick(ctx, s.opts.Now()); err != nil && ctx.Err() == nil {
			s.opts.Logger.Error("schedules", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
