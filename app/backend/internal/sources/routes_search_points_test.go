package sources

import (
	"testing"

	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
)

// The screens sort the rules that cost points, so a reason carries its points as a number too.
func TestReleaseViewReasonCarriesItsPoints(t *testing.T) {
	v := releaseView(domain.Release{Verdict: map[string]any{"reasons": []any{
		map[string]any{"step": "points", "rule": "trait:own:174", "effect": "subtract", "points": float64(-50000), "detail": "Anime Raws: fits"},
		map[string]any{"step": "points", "rule": "below_min", "effect": "fail", "detail": "-50000 points, needs at least 10000"},
	}}})
	if len(v.Reasons) != 2 {
		t.Fatalf("reasons = %+v", v.Reasons)
	}
	if v.Reasons[0].Points != -50000 || v.Reasons[0].Text != "Anime Raws: fits (-50000 points)" {
		t.Errorf("trait reason = %+v", v.Reasons[0])
	}
	if v.Reasons[1].Points != 0 {
		t.Errorf("fail reason points = %d, want 0", v.Reasons[1].Points)
	}
}
