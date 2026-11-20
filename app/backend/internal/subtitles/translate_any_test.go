package subtitles

import (
	"context"
	"os"
	"testing"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/jobs"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/sites/fakesite"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate/faketranslator"
)

// "Translate from: any language" takes whatever text subtitle the video has:
// here the only one is the English track inside it, and nobody named English.
func TestTranslateFromAnyLanguageUsesWhatTheVideoHas(t *testing.T) {
	f := newFix(t)
	track := labMovie(t, f)
	s, q := withTranslator(t, f, &faketranslator.OpenAI{})
	ctx := context.Background()
	cur := s.Translator(ctx)
	if _, err := s.SaveTranslator(ctx, TranslatorInput{Kind: cur.Kind, URL: cur.URL, Model: cur.Model, BuiltInAuto: true}, domain.System); err != nil {
		t.Fatal(err)
	}
	wishAll(t, f, Want{Language: "pt-br", TranslateFrom: FromAny})
	res, err := s.Search(ctx, f.movieFile, SearchOptions{}, domain.System)
	if err != nil || len(res.Outcomes) != 1 || res.Outcomes[0].Result != "translating" || len(*q) != 1 {
		t.Fatalf("search: %+v %v %+v", res.Outcomes, err, *q)
	}
	p := (*q)[0].params
	if p["source"] != SourceBuiltIn || p["track"] != float64(track) || p["language"] != "pt-br" {
		t.Fatalf("params %v", p)
	}
	if from, ok := p["from"]; ok && from != "" {
		t.Fatalf("any language must not name a source language, got %v", from)
	}
	r := &jobs.Run{Job: jobs.Job{Subject: &domain.Subject{Type: "file", ID: f.movieFile}, Params: p}}
	if err := s.handleTranslate(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.abs("The Lantern (2010)/The Lantern (2010) 1080p BluRay.pt-BR.srt")); err != nil {
		t.Fatal(err)
	}
}

// With a subtitle in another language beside the video, any language takes
// that file; a named language that is not there takes nothing.
func TestTranslateFromAnyLanguageTakesAnotherLanguagesFile(t *testing.T) {
	f := newFix(t)
	s, q := withTranslator(t, f, &faketranslator.OpenAI{})
	ctx := context.Background()
	beside := "The Lantern (2010)/The Lantern (2010) 1080p BluRay.ja.srt"
	if err := os.WriteFile(f.abs(beside), []byte(fakesite.Cue("こんにちは", 1000)), 0o644); err != nil {
		t.Fatal(err)
	}
	wishAll(t, f, Want{Language: "fr", TranslateFrom: "en"})
	if res, err := s.Search(ctx, f.movieFile, SearchOptions{}, domain.System); err != nil || len(*q) != 0 {
		t.Fatalf("from English with only Japanese there: %+v %v %+v", res.Outcomes, err, *q)
	}
	wishAll(t, f, Want{Language: "fr", TranslateFrom: FromAny})
	res, err := s.Search(ctx, f.movieFile, SearchOptions{}, domain.System)
	if err != nil || len(*q) != 1 || res.Outcomes[len(res.Outcomes)-1].Result != "translating" {
		t.Fatalf("from any language: %+v %v %+v", res.Outcomes, err, *q)
	}
	if p := (*q)[0].params; p["source"] != SourceBeside || p["path"] != beside {
		t.Fatalf("params %v", p)
	}
	// The translation itself reads the Japanese file and says so.
	out, err := s.Translate(ctx, f.movieFile, TranslateRequest{Language: "fr"}, domain.System)
	if err != nil || out.From != "ja" || out.Source != SourceBeside {
		t.Fatalf("translate: %+v %v", out, err)
	}
}

// The wanted language itself is never a source, whatever "any" finds.
func TestTranslateFromAnyLanguageNeverTakesTheWantedLanguage(t *testing.T) {
	f := newFix(t)
	labMovie(t, f)
	s, q := withTranslator(t, f, &faketranslator.OpenAI{})
	ctx := context.Background()
	cur := s.Translator(ctx)
	if _, err := s.SaveTranslator(ctx, TranslatorInput{Kind: cur.Kind, URL: cur.URL, Model: cur.Model, BuiltInAuto: true}, domain.System); err != nil {
		t.Fatal(err)
	}
	// English is wanted and the video already has English inside: nothing is missing, nothing is queued.
	wishAll(t, f, Want{Language: "en", TranslateFrom: FromAny})
	if _, err := s.Search(ctx, f.movieFile, SearchOptions{}, domain.System); err != nil || len(*q) != 0 {
		t.Fatalf("queued %+v %v", *q, err)
	}
}
