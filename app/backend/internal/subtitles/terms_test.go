package subtitles

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/transcribe"
)

// besideSub writes a subtitle beside the show's first episode and records it
// the way a search would have: made by a person unless made says otherwise.
func besideSub(t *testing.T, f *fix, name, lang, made string, lines ...string) {
	t.Helper()
	rel := "Quiet Harbour (2019)/Season 01/" + name
	var sb strings.Builder
	for i, l := range lines {
		at := i * 3
		fmt.Fprintf(&sb, "%d\n00:%02d:%02d,000 --> 00:%02d:%02d,500\n%s\n\n", i+1, at/60, at%60, (at+2)/60, (at+2)%60, l)
	}
	if err := os.MkdirAll(filepath.Dir(f.abs(rel)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.abs(rel), []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	var m any
	if made != "" {
		m = made
	}
	ts := database.FormatTime(now())
	subPinExec(t, f.st, `INSERT INTO subtitles (created_at, created_by, updated_at, updated_by, file_id, language, place, relative_path, origin, state, made)
		VALUES (?, ?, ?, ?, ?, ?, 'beside', ?, 'found', 'present', ?)`, ts, sysActor, ts, sysActor, f.ep1File, lang, rel, m)
}

func has(list []string, term string) bool {
	for _, x := range list {
		if x == term {
			return true
		}
	}
	return false
}

func TestTermsOfFindsNamesAndInventedWords(t *testing.T) {
	text := "Tell Captain Orlav the ferry is late.\nHe went to the Vale of Thorns, and Mirelle followed him.\n- Nobody saw Orlav leave.\nThe harbour was quiet. I think Mirelle knows."
	got := termsOf(text, "en")
	for _, want := range []string{"Captain Orlav", "Orlav", "Mirelle", "Vale of Thorns"} {
		if !has(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
	for _, not := range []string{"Tell", "He", "The", "Nobody", "I"} {
		if has(got, not) {
			t.Fatalf("%q is a sentence's first word or an ordinary word, not a term: %q", not, got)
		}
	}
}

func TestTermsOfKatakanaRuns(t *testing.T) {
	got := termsOf("それはカゼノヤイバだ。\nアオイが来た。", "ja")
	if !has(got, "カゼノヤイバ") || !has(got, "アオイ") {
		t.Fatalf("got %q", got)
	}
	if len(termsOf("这是一个测试。", "zh")) != 0 {
		t.Fatal("nothing is gathered for languages without capitals or katakana")
	}
}

func TestGatherNeedsTwoSubtitlesOrThreeUses(t *testing.T) {
	f := newFix(t)
	s := &Service{Store: f.st}
	ctx := context.Background()
	besideSub(t, f, "a.en.srt", "en", "", "We sail with Captain Orlav at dawn.", "Has anyone seen Mirelle today?")
	// One subtitle only: a term needs three uses.
	if got, _ := s.gatherTerms(ctx, f.show, "en"); len(got) != 0 {
		t.Fatalf("one use in one subtitle: %q", got)
	}
	besideSub(t, f, "b.en.srt", "en", "", "They say Orlav never sleeps.", "It was only Brannick again.")
	besideSub(t, f, "c.en.srt", "en", "", "Did you hear what Orlav said?", "Ask the cook.")
	s.forgetTerms()
	got, err := s.gatherTerms(ctx, f.show, "en")
	if err != nil || !has(got, "Orlav") || has(got, "Brannick") || has(got, "Mirelle") {
		t.Fatalf("got %q %v", got, err)
	}
	// Another language's subtitles are not read.
	if fr, _ := s.gatherTerms(ctx, f.show, "fr"); len(fr) != 0 {
		t.Fatalf("French terms from English subtitles: %q", fr)
	}
}

func TestGatherThreeUsesInOneSubtitle(t *testing.T) {
	f := newFix(t)
	s := &Service{Store: f.st}
	besideSub(t, f, "a.en.srt", "en", "", "We sail with Orlav at dawn.", "Has anyone seen Orlav today?", "They say Orlav never sleeps.", "It was only Brannick again.")
	got, _ := s.gatherTerms(context.Background(), f.show, "en")
	if !has(got, "Orlav") || has(got, "Brannick") {
		t.Fatalf("got %q", got)
	}
}

func TestGatherIgnoresMachineMadeSubtitles(t *testing.T) {
	f := newFix(t)
	s := &Service{Store: f.st}
	for i, made := range []string{MadeTranslated, "ai_translated", "machine_translated"} {
		besideSub(t, f, fmt.Sprintf("m%d.en.srt", i), "en", made, "We sail with Zorvath at dawn.", "Zorvath said so.", "Ask Zorvath.")
	}
	if got, _ := s.gatherTerms(context.Background(), f.show, "en"); len(got) != 0 {
		t.Fatalf("terms from machine-made subtitles: %q", got)
	}
}

func TestGatherRanksByEpisodesAndCaps(t *testing.T) {
	f := newFix(t)
	s := &Service{Store: f.st}
	for i := 0; i < 4; i++ {
		lines := []string{"We sail with Orlav at dawn."}
		if i < 2 {
			lines = append(lines, "Has anyone seen Mirelle today?")
		}
		for k := 0; k < 100 && i < 3; k++ { // in three of the four: fewer than Orlav
			lines = append(lines, fmt.Sprintf("We met Name%c%c in the square.", 'a'+k/26, 'a'+k%26))
		}
		besideSub(t, f, fmt.Sprintf("r%d.en.srt", i), "en", "", lines...)
	}
	got, _ := s.gatherTerms(context.Background(), f.show, "en")
	if len(got) != MaxGatheredTerms || got[0] != "Orlav" {
		t.Fatalf("%d terms, first %q", len(got), got[0])
	}
}

func TestPromptPutsOwnFirstAndCutsToRoom(t *testing.T) {
	f := newFix(t)
	s := &Service{Store: f.st}
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		var lines []string
		for k := 0; k < 120; k++ {
			lines = append(lines, fmt.Sprintf("We met Gathered%c%c in the square.", 'a'+k/26, 'a'+k%26))
		}
		besideSub(t, f, fmt.Sprintf("p%d.en.srt", i), "en", "", lines...)
	}
	own := []string{"Wind Blade Technique", "Aoi Kisaragi"}
	if _, err := s.SaveTerms(ctx, f.show, "en", own, nil, domain.System); err != nil {
		t.Fatal(err)
	}
	fc, err := s.load(ctx, f.ep1File)
	if err != nil {
		t.Fatal(err)
	}
	p := s.promptFor(ctx, fc, "en")
	if !strings.HasPrefix(p, "Wind Blade Technique, Aoi Kisaragi, ") || len(p) > transcribe.MaxPrompt || !strings.Contains(p, "Gathered") {
		t.Fatalf("prompt (%d chars) %q", len(p), p[:min(len(p), 120)])
	}
	// The title's own name is among the last things added when there is room.
	s.forgetTerms()
	subPinExec(t, f.st, "DELETE FROM subtitles WHERE file_id = ?", f.ep1File)
	if p := s.promptFor(ctx, fc, "en"); !strings.Contains(p, "Quiet Harbour") || !strings.HasPrefix(p, "Wind Blade Technique") {
		t.Fatalf("prompt %q", p)
	}
}

func TestPromptCleansOddTerms(t *testing.T) {
	f := newFix(t)
	s := &Service{Store: f.st}
	ctx := context.Background()
	got, err := s.SaveTerms(ctx, f.show, "en", []string{"  --model x ", "Two\nLines", strings.Repeat("y", 500), "Orlav", "orlav", "", "Orlav"}, nil, domain.System)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"model x", "Two Lines", "Orlav"}
	if strings.Join(got.Own, "|") != strings.Join(want, "|") {
		t.Fatalf("own %q", got.Own)
	}
	fc, _ := s.load(ctx, f.ep1File)
	if p := s.promptFor(ctx, fc, "en"); strings.ContainsAny(p, "\n\r\t") || strings.HasPrefix(p, "-") {
		t.Fatalf("prompt %q", p)
	}
}

func TestRemovedTermsStayOut(t *testing.T) {
	f := newFix(t)
	s := &Service{Store: f.st}
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		besideSub(t, f, fmt.Sprintf("x%d.en.srt", i), "en", "", "We sail with Orlav at dawn.", "Has anyone seen Mirelle today?")
	}
	got, err := s.SaveTerms(ctx, f.show, "en", nil, []string{"Mirelle"}, domain.System)
	if err != nil || has(got.Gathered, "Mirelle") || !has(got.Gathered, "Orlav") || !has(got.Removed, "Mirelle") {
		t.Fatalf("%+v %v", got, err)
	}
	again, _ := s.Terms(ctx, f.show, "en")
	if has(again.Gathered, "Mirelle") {
		t.Fatalf("a removed term came back: %+v", again)
	}
}
