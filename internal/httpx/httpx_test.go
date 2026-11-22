package httpx

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/lucaszago/meal-planner/internal/domain"
)

func TestStatusMapsWrappedErrors(t *testing.T) {
	cases := map[error]int{
		fmt.Errorf("plan: %w", domain.ErrNotFound):       http.StatusNotFound,
		&domain.ValidationError{Problems: []string{"x"}}: http.StatusUnprocessableEntity,
		fmt.Errorf("claude: %w", domain.ErrGeneration):   http.StatusBadGateway,
		fmt.Errorf("boom"): http.StatusInternalServerError,
	}
	for err, want := range cases {
		if got, _ := Status(err); got != want {
			t.Errorf("Status(%v) = %d, want %d", err, got, want)
		}
	}
}
