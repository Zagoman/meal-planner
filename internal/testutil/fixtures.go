package testutil

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Zagoman/meal-planner/internal/db"
	"github.com/Zagoman/meal-planner/internal/domain"
)

// NewHousehold creates a household with a default profile and sections and
// one admin member, and returns that member's scope. Isolation tests create
// two and check that one cannot see the other.
func NewHousehold(t testing.TB, pool *pgxpool.Pool, name string) domain.Scope {
	t.Helper()
	ctx := context.Background()
	q := db.New(pool)
	h, err := q.CreateHousehold(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	m, err := q.CreateMember(ctx, db.CreateMemberParams{
		Email: uuid.NewString()[:8] + "@" + "example.test",
		Name:  name + " admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.AddHouseholdMember(ctx, db.AddHouseholdMemberParams{
		HouseholdID: h.ID, MemberID: m.ID, Role: string(domain.RoleAdmin),
	}); err != nil {
		t.Fatal(err)
	}
	if err := q.CreateProfile(ctx, h.ID); err != nil {
		t.Fatal(err)
	}
	for _, s := range domain.DefaultSections {
		if err := q.InsertSection(ctx, db.InsertSectionParams{
			HouseholdID: h.ID, Key: s.Key, Name: s.Name, SortOrder: int32(s.SortOrder), //nolint:gosec // small constant
		}); err != nil {
			t.Fatal(err)
		}
	}
	return domain.Scope{HouseholdID: h.ID, MemberID: m.ID, Role: domain.RoleAdmin}
}
