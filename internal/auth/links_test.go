package auth_test

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/Zagoman/meal-planner/internal/auth"
	"github.com/Zagoman/meal-planner/internal/db"
	"github.com/Zagoman/meal-planner/internal/domain"
	"github.com/Zagoman/meal-planner/internal/household"
	"github.com/Zagoman/meal-planner/internal/mail"
	"github.com/Zagoman/meal-planner/internal/testutil"
)

var linkRe = regexp.MustCompile(`https://meals\.example\.test/auth/([A-Za-z0-9_-]{43})`)

func linkFrom(t *testing.T, rec *mail.Recorder, to string) string {
	t.Helper()
	m, err := rec.Last(to)
	if err != nil {
		t.Fatal(err)
	}
	if m.Subject != "Your sign-in link" {
		t.Errorf("subject %q", m.Subject)
	}
	match := linkRe.FindStringSubmatch(m.Text)
	if match == nil {
		t.Fatalf("no link in %q", m.Text)
	}
	return match[1]
}

func TestSignInLink(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewDB(t)
	rec := &mail.Recorder{}
	a := auth.New(pool, "").WithMail(rec, "https://meals.example.test/")
	h := testutil.NewHousehold(t, pool, "Alpha")
	m, err := a.Register(ctx, "ana@example.test", "Ana", pw)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.New(pool).AddHouseholdMember(ctx, db.AddHouseholdMemberParams{HouseholdID: h.HouseholdID, MemberID: m.ID, Role: "member"}); err != nil {
		t.Fatal(err)
	}

	// Unknown address: the same answer, no email.
	if err := a.RequestLoginLink(ctx, "nobody@example.test"); err != nil {
		t.Errorf("unknown address: %v", err)
	}
	if err := a.RequestLoginLink(ctx, "not an email"); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("bad address: %v", err)
	}
	if err := a.RequestLoginLink(ctx, " ANA@example.test "); err != nil {
		t.Fatal(err)
	}
	// A second request within a minute sends nothing.
	if err := a.RequestLoginLink(ctx, "ana@example.test"); err != nil {
		t.Fatal(err)
	}
	if len(rec.Sent) != 1 {
		t.Fatalf("%d emails, want 1: %+v", len(rec.Sent), rec.Sent)
	}
	tok := linkFrom(t, rec, "ana@example.test")

	session, sc, err := a.SignInWithLink(ctx, tok)
	if err != nil || sc.HouseholdID != h.HouseholdID || sc.MemberID != m.ID {
		t.Fatalf("sign in: %+v %v", sc, err)
	}
	if got, err := a.SessionScope(ctx, session); err != nil || got.MemberID != m.ID {
		t.Errorf("session: %+v %v", got, err)
	}
	// Single use.
	if _, _, err := a.SignInWithLink(ctx, tok); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("second use: %v", err)
	}
	if _, _, err := a.SignInWithLink(ctx, "nonsense"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("bad token: %v", err)
	}
	// Expired after 15 minutes.
	if _, err := pool.Exec(ctx, `UPDATE login_tokens SET created_at = now() - interval '2 minutes'`); err != nil {
		t.Fatal(err)
	}
	if err := a.RequestLoginLink(ctx, "ana@example.test"); err != nil {
		t.Fatal(err)
	}
	tok2 := linkFrom(t, rec, "ana@example.test")
	if _, err := pool.Exec(ctx, `UPDATE login_tokens SET expires_at = now() - interval '1 second' WHERE used_at IS NULL`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.SignInWithLink(ctx, tok2); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expired link: %v", err)
	}
	// Only the hash is stored.
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM login_tokens WHERE token_hash = $1`, []byte(tok)).Scan(&n); err != nil || n != 0 {
		t.Errorf("raw token stored: %d %v", n, err)
	}
}

func TestJoinFromInvite(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewDB(t)
	rec := &mail.Recorder{}
	a := auth.New(pool, "").WithMail(rec, "https://meals.example.test")
	hh := household.New(pool, "https://meals.example.test", "")
	admin := testutil.NewHousehold(t, pool, "Beta")
	inv, err := hh.CreateInvite(ctx, admin, domain.RoleMember)
	if err != nil {
		t.Fatal(err)
	}

	p, err := a.InvitePreview(ctx, inv.Token)
	if err != nil || p.HouseholdName != "Beta" {
		t.Fatalf("preview: %+v %v", p, err)
	}
	if err := a.Join(ctx, inv.Token, "", "x"); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("bad input: %v", err)
	}
	if err := a.Join(ctx, "nope", "Bo", "bo@example.test"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("bad invite: %v", err)
	}
	if err := a.Join(ctx, inv.Token, "Bo", "Bo@Example.test"); err != nil {
		t.Fatal(err)
	}
	// Not a member until the link is used.
	q := db.New(pool)
	bo, err := q.GetMemberByEmail(ctx, "bo@example.test")
	if err != nil || bo.Name != "Bo" || bo.PasswordHash != "" {
		t.Fatalf("member: %+v %v", bo, err)
	}
	if _, err := q.GetMembership(ctx, db.GetMembershipParams{HouseholdID: admin.HouseholdID, MemberID: bo.ID}); err == nil {
		t.Fatal("membership added before the link was used")
	}
	tok := linkFrom(t, rec, "bo@example.test")
	_, sc, err := a.SignInWithLink(ctx, tok)
	if err != nil || sc.HouseholdID != admin.HouseholdID || sc.Role != domain.RoleMember {
		t.Fatalf("sign in from invite: %+v %v", sc, err)
	}
	// The invite is used up: the page and a second join say so.
	if _, err := a.InvitePreview(ctx, inv.Token); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("used invite preview: %v", err)
	}
	if err := a.Join(ctx, inv.Token, "Cy", "cy@example.test"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("used invite join: %v", err)
	}
}

// Two people answer the same invite before either signs in: the first link
// used joins, the second is refused.
func TestInviteUsedOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewDB(t)
	rec := &mail.Recorder{}
	a := auth.New(pool, "").WithMail(rec, "https://meals.example.test")
	hh := household.New(pool, "https://meals.example.test", "")
	admin := testutil.NewHousehold(t, pool, "Gamma")
	inv, _ := hh.CreateInvite(ctx, admin, domain.RoleMember)
	for _, e := range []string{"one@example.test", "two@example.test"} {
		if err := a.Join(ctx, inv.Token, "X", e); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := a.SignInWithLink(ctx, linkFrom(t, rec, "one@example.test")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.SignInWithLink(ctx, linkFrom(t, rec, "two@example.test")); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("second person: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM household_members WHERE household_id = $1`, admin.HouseholdID).Scan(&n); err != nil || n != 2 {
		t.Errorf("%d members, want 2 (%v)", n, err)
	}
}

func TestCreateHouseholdWithInvite(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewDB(t)
	hh := household.New(pool, "https://meals.example.test", "")
	if _, err := hh.Create(ctx, domain.Scope{}, "New", ""); !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("non-admin: %v", err)
	}
	h, err := hh.Create(ctx, domain.Scope{ServerAdmin: true}, "New home", "")
	if err != nil || h.Invite == nil || h.Invite.Role != domain.RoleAdmin || h.Invite.URL != "https://meals.example.test/join/"+h.Invite.Token {
		t.Fatalf("create: %+v %v", h, err)
	}
	p, err := hh.Get(ctx, domain.Scope{HouseholdID: h.ID})
	if err != nil || p.SetupDone {
		t.Errorf("new household should need setup: %+v %v", p, err)
	}
}
