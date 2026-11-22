package auth_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Zagoman/meal-planner/internal/auth"
	"github.com/Zagoman/meal-planner/internal/domain"
	"github.com/Zagoman/meal-planner/internal/household"
	"github.com/Zagoman/meal-planner/internal/testutil"
)

func TestSignUpWithInvite(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewDB(t)
	a := auth.New(pool, "")
	h := household.New(pool, "http://localhost", "")
	admin := testutil.NewHousehold(t, pool, "Alpha")

	inv, err := h.CreateInvite(ctx, admin, domain.RoleMember)
	if err != nil {
		t.Fatal(err)
	}

	// Bad input creates nothing and keeps the invite usable.
	if _, _, err := a.SignUpWithInvite(ctx, inv.Token, "not-an-email", "Bo", "short"); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("bad input: %v", err)
	}
	if _, _, err := a.SignUpWithInvite(ctx, "no-such-token", "bo@example.test", "Bo", pw); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown invite: %v", err)
	}

	tok, sc, err := a.SignUpWithInvite(ctx, inv.Token, "Bo@Example.test", "Bo", pw)
	if err != nil {
		t.Fatal(err)
	}
	if sc.HouseholdID != admin.HouseholdID || sc.Role != domain.RoleMember {
		t.Fatalf("scope: %+v", sc)
	}
	if got, err := a.SessionScope(ctx, tok); err != nil || got.MemberID != sc.MemberID {
		t.Fatalf("session: %+v %v", got, err)
	}
	if _, _, err := a.Login(ctx, "bo@example.test", pw); err != nil {
		t.Fatalf("login after sign-up: %v", err)
	}

	// The invite is single use, and a used invite leaves no extra account.
	if _, _, err := a.SignUpWithInvite(ctx, inv.Token, "cy@example.test", "Cy", pw); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("reused invite: %v", err)
	}
	if _, _, err := a.Login(ctx, "cy@example.test", pw); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("cy should not exist: %v", err)
	}

	// An existing email is a conflict and does not use up the invite.
	inv2, err := h.CreateInvite(ctx, admin, domain.RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.SignUpWithInvite(ctx, inv2.Token, "bo@example.test", "Bo", pw); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("existing email: %v", err)
	}
	if _, _, err := a.SignUpWithInvite(ctx, inv2.Token, "di@example.test", "Di", pw); err != nil {
		t.Fatalf("invite should still be usable: %v", err)
	}
}

func TestSignUpWithInviteConcurrent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewDB(t)
	a := auth.New(pool, "")
	admin := testutil.NewHousehold(t, pool, "Alpha")
	inv, err := household.New(pool, "http://localhost", "").CreateInvite(ctx, admin, domain.RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for _, e := range []string{"e1@example.test", "e2@example.test", "e3@example.test", "e4@example.test"} {
		wg.Add(1)
		go func(email string) {
			defer wg.Done()
			if _, _, err := a.SignUpWithInvite(ctx, inv.Token, email, "x", pw); err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}(e)
	}
	wg.Wait()
	if ok != 1 {
		t.Fatalf("%d sign-ups succeeded with one invite, want 1", ok)
	}
}
