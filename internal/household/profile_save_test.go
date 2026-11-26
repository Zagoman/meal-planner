package household_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Zagoman/meal-planner/internal/domain"
)

// Handoff item 8: the protein split can't add up to more than the dinners.
func TestPutProteinAboveDinners(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	p, err := f.s.Get(ctx, f.a)
	if err != nil {
		t.Fatal(err)
	}
	p.Avoid = nil
	p.MealTargets[domain.MealDinner] = 4
	p.ProteinMix = map[domain.Protein]int{domain.ProteinChicken: 3, domain.ProteinFish: 2}
	_, err = f.s.Put(ctx, f.a, p)
	var ve *domain.ValidationError
	if !errors.As(err, &ve) || !strings.Contains(strings.Join(ve.Problems, ";"), "protein_mix:") {
		t.Fatalf("5 of 4: %v", err)
	}
	p.ProteinMix = map[domain.Protein]int{domain.ProteinChicken: 2, domain.ProteinFish: 2}
	got, err := f.s.Put(ctx, f.a, p)
	if err != nil || got.ProteinMix[domain.ProteinFish] != 2 {
		t.Fatalf("4 of 4: %+v %v", got.ProteinMix, err)
	}
	// Lowering dinners below the split is refused as well.
	p.MealTargets[domain.MealDinner] = 3
	if _, err := f.s.Put(ctx, f.a, p); !errors.As(err, &ve) {
		t.Errorf("dinners 3 with 4 set: %v", err)
	}
	// The other household is untouched.
	if b, _ := f.s.Get(ctx, f.b); len(b.ProteinMix) != 0 {
		t.Errorf("other household's protein: %v", b.ProteinMix)
	}
}

func TestSetMyName(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	m, err := f.s.SetMyName(ctx, f.a, "  Alma   Jensen ")
	if err != nil || m.Name != "Alma Jensen" || m.ID != f.a.MemberID || m.Role != domain.RoleAdmin {
		t.Fatalf("set: %+v %v", m, err)
	}
	if name := nameOf(t, f, f.a, f.a); name != "Alma Jensen" {
		t.Errorf("stored: %q", name)
	}
	if name := nameOf(t, f, f.b, f.b); name != "Beta admin" {
		t.Errorf("other household's member renamed: %q", name)
	}

	var ve *domain.ValidationError
	if _, err := f.s.SetMyName(ctx, f.a, "   "); !errors.As(err, &ve) {
		t.Errorf("empty: %v", err)
	}
	if _, err := f.s.SetMyName(ctx, f.a, strings.Repeat("x", 101)); !errors.As(err, &ve) {
		t.Errorf("too long: %v", err)
	}

	// Rule 1: a scope pairing a member with a household they are not in is
	// not found, and nothing changes.
	cross := domain.Scope{HouseholdID: f.b.HouseholdID, MemberID: f.a.MemberID, Role: domain.RoleAdmin}
	if _, err := f.s.SetMyName(ctx, cross, "Intruder"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("cross household: %v", err)
	}
	if name := nameOf(t, f, f.a, f.a); name != "Alma Jensen" {
		t.Errorf("after cross: %q", name)
	}
}

func nameOf(t *testing.T, f fixture, sc, who domain.Scope) string {
	t.Helper()
	ms, err := f.s.Members(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.ID == who.MemberID {
			return m.Name
		}
	}
	t.Fatalf("member %s not in household", who.MemberID)
	return ""
}
