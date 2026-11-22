package auth_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/Zagoman/meal-planner/internal/auth"
	"github.com/Zagoman/meal-planner/internal/db"
	"github.com/Zagoman/meal-planner/internal/domain"
	"github.com/Zagoman/meal-planner/internal/testutil"
)

const pw = "correct horse"

func TestLoginSessionLogout(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewDB(t)
	q := db.New(pool)
	a := auth.New(pool, "")
	hA := testutil.NewHousehold(t, pool, "Alpha")

	m, err := a.Register(ctx, " Ana@Example.test ", "Ana", pw)
	if err != nil {
		t.Fatal(err)
	}
	if m.Email != "ana@example.test" {
		t.Errorf("email not normalized: %q", m.Email)
	}
	// No household yet: cannot log in.
	if _, _, err := a.Login(ctx, "ana@example.test", pw); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("login without household: %v", err)
	}
	if err := q.AddHouseholdMember(ctx, db.AddHouseholdMemberParams{HouseholdID: hA.HouseholdID, MemberID: m.ID, Role: "member"}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][2]string{{"ana@example.test", "wrong password"}, {"nobody@example.test", pw}, {"ana@example.test", ""}} {
		if _, _, err := a.Login(ctx, bad[0], bad[1]); !errors.Is(err, domain.ErrUnauthorized) {
			t.Errorf("login %v: %v", bad, err)
		}
	}
	tok, sc, err := a.Login(ctx, "ANA@example.test", pw)
	if err != nil {
		t.Fatal(err)
	}
	if sc.HouseholdID != hA.HouseholdID || sc.MemberID != m.ID || sc.Role != domain.RoleMember || sc.ServerAdmin {
		t.Errorf("scope: %+v", sc)
	}
	// Only the hash is stored.
	var stored []byte
	if err := pool.QueryRow(ctx, "SELECT token_hash FROM sessions WHERE member_id = $1", m.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(tok))
	if !bytes.Equal(stored, sum[:]) || bytes.Contains(stored, []byte(tok)) {
		t.Errorf("stored token_hash is not sha256(token)")
	}
	got, err := a.SessionScope(ctx, tok)
	if err != nil || got != sc {
		t.Fatalf("session scope: %+v %v", got, err)
	}
	if _, err := a.SessionScope(ctx, tok+"x"); !errors.Is(err, domain.ErrUnauthorized) {
		t.Errorf("tampered token: %v", err)
	}
	if _, err := a.SessionScope(ctx, ""); !errors.Is(err, domain.ErrUnauthorized) {
		t.Errorf("empty token: %v", err)
	}
	if err := a.Logout(ctx, tok); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SessionScope(ctx, tok); !errors.Is(err, domain.ErrUnauthorized) {
		t.Errorf("after logout: %v", err)
	}
	if err := a.Logout(ctx, tok); err != nil {
		t.Errorf("second logout: %v", err)
	}

	// Expired sessions do not work.
	tok, _, err = a.Login(ctx, "ana@example.test", pw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE sessions SET expires_at = now() - interval '1 second'"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SessionScope(ctx, tok); !errors.Is(err, domain.ErrUnauthorized) {
		t.Errorf("expired session: %v", err)
	}
}

func TestRegisterValidation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a := auth.New(testutil.NewDB(t), "")
	if _, err := a.Register(ctx, "dup@example.test", "", pw); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Register(ctx, "DUP@example.test", "", pw); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("duplicate: %v", err)
	}
	for _, c := range [][2]string{{"not-an-email", pw}, {"x@example.test", "short"}, {"y@example.test", string(make([]byte, 73))}} {
		if _, err := a.Register(ctx, c[0], "", c[1]); !errors.Is(err, domain.ErrValidation) {
			t.Errorf("register %q: %v", c[0], err)
		}
	}
}

func TestSwitchHousehold(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewDB(t)
	q := db.New(pool)
	a := auth.New(pool, "")
	hA := testutil.NewHousehold(t, pool, "Alpha")
	hB := testutil.NewHousehold(t, pool, "Beta")
	other := testutil.NewHousehold(t, pool, "Other")

	m, err := a.Register(ctx, "two@example.test", "", pw)
	if err != nil {
		t.Fatal(err)
	}
	for h, role := range map[uuid.UUID]string{hA.HouseholdID: "member", hB.HouseholdID: "admin"} {
		if err := q.AddHouseholdMember(ctx, db.AddHouseholdMemberParams{HouseholdID: h, MemberID: m.ID, Role: role}); err != nil {
			t.Fatal(err)
		}
	}
	tok, sc, err := a.Login(ctx, "two@example.test", pw)
	if err != nil {
		t.Fatal(err)
	}
	if sc.HouseholdID != hA.HouseholdID { // first by name
		t.Fatalf("login household: %v", sc.HouseholdID)
	}
	sc, err = a.SwitchHousehold(ctx, tok, hB.HouseholdID)
	if err != nil || sc.HouseholdID != hB.HouseholdID || sc.Role != domain.RoleAdmin {
		t.Fatalf("switch: %+v %v", sc, err)
	}
	if got, _ := a.SessionScope(ctx, tok); got != sc {
		t.Errorf("session not switched: %+v", got)
	}
	// Not a member of Other: not found, session unchanged.
	if _, err := a.SwitchHousehold(ctx, tok, other.HouseholdID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("switch to foreign household: %v", err)
	}
	if _, err := a.SwitchHousehold(ctx, tok, uuid.New()); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("switch to unknown household: %v", err)
	}
	if got, _ := a.SessionScope(ctx, tok); got.HouseholdID != hB.HouseholdID {
		t.Errorf("session moved: %+v", got)
	}
	if _, err := a.SwitchHousehold(ctx, "bogus", hA.HouseholdID); !errors.Is(err, domain.ErrUnauthorized) {
		t.Errorf("bogus session: %v", err)
	}
	// Removed from a household: its session stops working.
	if err := q.RemoveHouseholdMember(ctx, db.RemoveHouseholdMemberParams{HouseholdID: hB.HouseholdID, MemberID: m.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SessionScope(ctx, tok); !errors.Is(err, domain.ErrUnauthorized) {
		t.Errorf("session after removal: %v", err)
	}
}

func TestAPITokens(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewDB(t)
	a := auth.New(pool, "")
	hA := testutil.NewHousehold(t, pool, "Alpha")
	hB := testutil.NewHousehold(t, pool, "Beta")

	tok, err := a.CreateAPIToken(ctx, hA, "script")
	if err != nil {
		t.Fatal(err)
	}
	sc, err := a.BearerScope(ctx, tok)
	if err != nil || sc != hA {
		t.Fatalf("bearer: %+v %v", sc, err)
	}
	if _, err := a.BearerScope(ctx, ""); !errors.Is(err, domain.ErrUnauthorized) {
		t.Errorf("empty: %v", err)
	}
	list, err := a.APITokens(ctx, hA)
	if err != nil || len(list) != 1 || list[0].Name != "script" || list[0].LastUsedAt == nil {
		t.Fatalf("list: %+v %v", list, err)
	}
	// B cannot see or revoke A's token.
	if l, _ := a.APITokens(ctx, hB); len(l) != 0 {
		t.Errorf("B sees A's tokens: %+v", l)
	}
	if err := a.RevokeAPIToken(ctx, hB, list[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.BearerScope(ctx, tok); err != nil {
		t.Errorf("B revoked A's token: %v", err)
	}
	if err := a.RevokeAPIToken(ctx, hA, list[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.BearerScope(ctx, tok); !errors.Is(err, domain.ErrUnauthorized) {
		t.Errorf("revoked token works: %v", err)
	}
	if _, err := a.CreateAPIToken(ctx, hA, string(make([]byte, 101))); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("long name: %v", err)
	}
}

func TestServerAdminAndResetPassword(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewDB(t)
	q := db.New(pool)
	a := auth.New(pool, "Boss@Example.test")
	h := testutil.NewHousehold(t, pool, "Alpha")
	m, err := q.CreateMember(ctx, db.CreateMemberParams{Email: "boss@example.test"}) // no password, flag not set
	if err != nil {
		t.Fatal(err)
	}
	if err := q.AddHouseholdMember(ctx, db.AddHouseholdMemberParams{HouseholdID: h.HouseholdID, MemberID: m.ID, Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.Login(ctx, "boss@example.test", ""); !errors.Is(err, domain.ErrUnauthorized) {
		t.Errorf("passwordless login: %v", err)
	}
	newPW, err := a.ResetPassword(ctx, "BOSS@example.test")
	if err != nil || len(newPW) < 20 {
		t.Fatalf("reset: %q %v", newPW, err)
	}
	_, sc, err := a.Login(ctx, "boss@example.test", newPW)
	if err != nil || !sc.ServerAdmin {
		t.Fatalf("admin login: %+v %v", sc, err)
	}
	if _, err := a.ResetPassword(ctx, "ghost@example.test"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("reset unknown: %v", err)
	}
	// Other members are not server admins.
	if _, sc, _ := a.Login(ctx, "boss@example.test", newPW); sc.MemberID == h.MemberID {
		t.Fatal("wrong member")
	}
	tok, _ := a.CreateAPIToken(ctx, h, "")
	if sc, _ := a.BearerScope(ctx, tok); sc.ServerAdmin {
		t.Errorf("ordinary member is server admin")
	}
}

func TestNewToken(t *testing.T) {
	t.Parallel()
	tok, hash := auth.NewToken()
	tok2, _ := auth.NewToken()
	sum := sha256.Sum256([]byte(tok))
	if len(tok) != 43 || tok == tok2 || !bytes.Equal(hash, sum[:]) {
		t.Fatalf("token %q", tok)
	}
}
