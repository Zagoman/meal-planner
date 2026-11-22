package domain

import "errors"

// Sentinel errors. Services wrap them (fmt.Errorf("...: %w", ErrNotFound));
// the API and web layers map them to HTTP status codes with errors.Is.
var (
	ErrNotFound       = errors.New("not found")         // 404; also used for other households' rows
	ErrUnauthorized   = errors.New("unauthorized")      // 401
	ErrForbidden      = errors.New("forbidden")         // 403
	ErrValidation     = errors.New("invalid input")     // 422
	ErrConflict       = errors.New("conflict")          // 409
	ErrGeneration     = errors.New("generation failed") // 502; the previous plan stays active
	ErrNotImplemented = errors.New("not implemented")   // 501
)

// ValidationError lists field problems. It matches ErrValidation with errors.Is.
type ValidationError struct {
	Problems []string
}

func (e *ValidationError) Error() string {
	if len(e.Problems) == 0 {
		return ErrValidation.Error()
	}
	msg := ErrValidation.Error() + ": " + e.Problems[0]
	for _, p := range e.Problems[1:] {
		msg += "; " + p
	}
	return msg
}

func (e *ValidationError) Is(target error) bool { return target == ErrValidation }
