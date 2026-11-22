package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Zagoman/meal-planner/internal/auth"
	"github.com/Zagoman/meal-planner/internal/db"
	"github.com/Zagoman/meal-planner/internal/domain"
	"github.com/Zagoman/meal-planner/internal/testutil"
)

func TestChangePassword(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewDB(t)
	a := auth.New(pool, "")
	h := testutil.NewHousehold(t, pool, "Alpha")
	m, err := a.Register(ctx, "ana@example.test", "Ana", pw)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.New(pool).AddHouseholdMember(ctx, db.AddHouseholdMemberParams{HouseholdID: h.HouseholdID, MemberID: m.ID, Role: "member"}); err != nil {
		t.Fatal(err)
	}
	phone, sc, err := a.Login(ctx, "ana@example.test", pw)
	if err != nil {
		t.Fatal(err)
	}
	laptop, _, err := a.Login(ctx, "ana@example.test", pw)
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct{ current, next string }{
		{"wrong password", "a new password"}, // wrong current
		{pw, "short"},                        // too short
		{pw, pw},                             // unchanged
	} {
		if err := a.ChangePassword(ctx, sc, phone, c.current, c.next); !errors.Is(err, domain.ErrValidation) {
			t.Errorf("ChangePassword(%q, %q) = %v, want validation error", c.current, c.next, err)
		}
	}
	if _, err := a.SessionScope(ctx, laptop); err != nil {
		t.Fatalf("a failed change signed the laptop out: %v", err)
	}

	if err := a.ChangePassword(ctx, sc, phone, pw, "a brand new password"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.Login(ctx, "ana@example.test", pw); !errors.Is(err, domain.ErrUnauthorized) {
		t.Errorf("old password still works: %v", err)
	}
	if _, _, err := a.Login(ctx, "ana@example.test", "a brand new password"); err != nil {
		t.Errorf("new password: %v", err)
	}
	if _, err := a.SessionScope(ctx, phone); err != nil {
		t.Errorf("the session that changed the password was signed out: %v", err)
	}
	if _, err := a.SessionScope(ctx, laptop); err == nil {
		t.Error("the other session is still signed in")
	}
}
