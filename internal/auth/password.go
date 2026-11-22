package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/Zagoman/meal-planner/internal/db"
	"github.com/Zagoman/meal-planner/internal/domain"
)

// ChangePassword lets a signed-in member replace their own password. The
// current password must be right (a wrong one is a ValidationError, so the
// form can say so). On success every other session of the member is signed
// out; the session with keepSessionToken stays. API tokens are not touched.
// It implements web.PasswordChanger.
func (s *Service) ChangePassword(ctx context.Context, sc domain.Scope, keepSessionToken, current, next string) error {
	m, err := s.q.GetMember(ctx, sc.MemberID)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("change password: %w", domain.ErrUnauthorized)
	}
	if err != nil {
		return fmt.Errorf("change password: %w", err)
	}
	if m.PasswordHash == "" || bcrypt.CompareHashAndPassword([]byte(m.PasswordHash), []byte(current)) != nil {
		return &domain.ValidationError{Problems: []string{"current password: not correct"}}
	}
	if p := passwordProblem(next); p != "" {
		return &domain.ValidationError{Problems: []string{"new " + p}}
	}
	if next == current {
		return &domain.ValidationError{Problems: []string{"new password: must differ from the current one"}}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(next), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("change password: %w", err)
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if err := q.UpdateMemberPassword(ctx, db.UpdateMemberPasswordParams{ID: m.ID, PasswordHash: string(hash)}); err != nil {
			return fmt.Errorf("change password: %w", err)
		}
		if _, err := q.DeleteOtherSessions(ctx, db.DeleteOtherSessionsParams{MemberID: m.ID, KeepTokenHash: HashToken(keepSessionToken)}); err != nil {
			return fmt.Errorf("change password: sign out other sessions: %w", err)
		}
		return nil
	})
}
