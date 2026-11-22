package subtitles

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate/faketranslator"
)

// innerGate stands in for the parts gate: it holds one kind of its own.
type innerGate struct{ on bool }

func (g innerGate) Active() bool { return g.on }
func (g innerGate) Held(_ context.Context, _ database.Querier, kind string, _ *database.Subject) bool {
	return g.on && kind == "music_scan"
}
func (g innerGate) KindOff(kind string) bool { return g.on && kind == "music_scan" }
func (g innerGate) OffKinds() []string {
	if g.on {
		return []string{"music_scan"}
	}
	return nil
}

// Paused, the subtitle work that searches, translates and transcribes waits
// in the queue; model downloads and every other part's jobs go on. The pause
// is kept across a restart.
func TestPauseHoldsSubtitleWorkAndNothingElse(t *testing.T) {
	f := newFix(t)
	s := &Service{Store: f.st}
	ctx := context.Background()
	g := PauseGate(innerGate{}, s)
	if g.Active() || s.Paused() {
		t.Fatal("paused before anyone asked")
	}
	if err := s.SetPaused(ctx, true, domain.System); err != nil {
		t.Fatal(err)
	}
	if !g.Active() || !s.Paused() {
		t.Fatal("not paused")
	}
	for kind, held := range map[string]bool{KindTranscribe: true, KindTranslate: true, KindSearch: true, KindMissing: true, KindBetter: true,
		KindModelDownload: false, KindLook: false, "grab": false, "music_scan": false} {
		if got := g.Held(ctx, f.st.Read(), kind, nil); got != held {
			t.Errorf("%s held = %v, want %v", kind, got, held)
		}
	}
	if !g.KindOff(KindMissing) || !g.KindOff(KindBetter) || g.KindOff("feed_check") {
		t.Fatal("the timed subtitle searches must not be queued while paused, and others must")
	}
	off := g.(interface{ OffKinds() []string }).OffKinds()
	sort.Strings(off)
	if strings.Join(off, ",") != strings.Join([]string{KindBetter, KindMissing, KindSearch, KindTranscribe, KindTranslate}, ",") {
		t.Fatalf("off kinds %v", off)
	}
	// A restart: a new service reads the pause back.
	s2 := &Service{Store: f.st}
	s2.LoadPaused(ctx)
	if !s2.Paused() {
		t.Fatal("the pause did not last a restart")
	}
	if err := s2.SetPaused(ctx, false, domain.System); err != nil || s2.Paused() || PauseGate(innerGate{}, s2).Active() {
		t.Fatalf("not resumed: %v", err)
	}
}

// The parts gate keeps its own say whether subtitles are paused or not.
func TestPauseGateKeepsThePartsGate(t *testing.T) {
	f := newFix(t)
	s := &Service{Store: f.st}
	ctx := context.Background()
	g := PauseGate(innerGate{on: true}, s)
	if !g.Active() || !g.Held(ctx, f.st.Read(), "music_scan", nil) || g.Held(ctx, f.st.Read(), KindTranscribe, nil) || !g.KindOff("music_scan") {
		t.Fatal("the parts gate alone")
	}
	_ = s.SetPaused(ctx, true, domain.System)
	off := g.(interface{ OffKinds() []string }).OffKinds()
	if len(off) != 6 || !g.Held(ctx, f.st.Read(), "music_scan", nil) || !g.Held(ctx, f.st.Read(), KindTranscribe, nil) {
		t.Fatalf("both: %v", off)
	}
	// With no parts gate at all the pause still works.
	if g := PauseGate(nil, s); !g.Active() || !g.Held(ctx, f.st.Read(), KindTranslate, nil) || g.Held(ctx, f.st.Read(), "grab", nil) {
		t.Fatal("no inner gate")
	}
}

// route: PUT /api/v1/subtitles/pause
func TestRoutes_Pause(t *testing.T) {
	f := newFix(t)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	m := http.NewServeMux()
	NewHandlers(f.st, s.Vault, s).Routes(m)
	if code, body := call(m, "GET", "/api/v1/subtitles/overview", ""); code != 200 || !strings.Contains(body, `"paused":false`) {
		t.Fatalf("overview before: %d %s", code, body)
	}
	if code, body := call(m, "PUT", "/api/v1/subtitles/pause", `{"paused":true}`); code != 200 || !strings.Contains(body, `"paused":true`) || !s.Paused() {
		t.Fatalf("pause: %d %s", code, body)
	}
	if code, body := call(m, "GET", "/api/v1/subtitles/overview", ""); code != 200 || !strings.Contains(body, `"paused":true`) {
		t.Fatalf("overview after: %d %s", code, body)
	}
	if code, _ := call(m, "PUT", "/api/v1/subtitles/pause", `{"paused":false}`); code != 200 || s.Paused() {
		t.Fatalf("resume: %d", code)
	}
	if code, _ := call(m, "PUT", "/api/v1/subtitles/pause", `not json`); code != http.StatusBadRequest {
		t.Fatalf("bad body: %d", code)
	}
}
