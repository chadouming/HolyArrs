package subtitles

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/subtext"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/transcribe"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate/faketranslator"
)

func TestDetectForeignCues_MixedEpisodeCues(t *testing.T) {
	cues := []subtext.Cue{
		{StartMS: 0, EndMS: 2000, Text: "previously on 90 Day Fiancé."},
		{StartMS: 2100, EndMS: 4500, Text: "I wish it wasn't a month from now..."},
		{StartMS: 4600, EndMS: 6500, Text: "Open the fucking curtain."},
		{StartMS: 6600, EndMS: 8500, Text: "Devin, what's going on?"},
		{StartMS: 8600, EndMS: 9500, Text: "No."},
		{StartMS: 9600, EndMS: 11500, Text: "This café has a cliché résumé."},
		{StartMS: 12000, EndMS: 14500, Text: "Realmente me preocupa mucho..."},
		{StartMS: 14600, EndMS: 16500, Text: "¿Estás listo?"},
		{StartMS: 16600, EndMS: 18000, Text: "¡Vamos!"},
		{StartMS: 18500, EndMS: 21000, Text: "Siento que este es el momento perfecto... de este"},
		{StartMS: 21100, EndMS: 22500, Text: "apartamento."},
		{StartMS: 23000, EndMS: 25500, Text: "Estoy muy entusiasmada... porque es"},
		{StartMS: 25600, EndMS: 27000, Text: "abrumante y..."},
		{StartMS: 27100, EndMS: 28500, Text: "Asfixiante."},
		{StartMS: 29000, EndMS: 32000, Text: "Yeah, pero se llama también, just like Cam."},
		{StartMS: 32500, EndMS: 35500, Text: "Yo te pido $100 enviarlas pa' mi familia."},
		{StartMS: 36000, EndMS: 38000, Text: "Hasta nunca."},
		{StartMS: 38500, EndMS: 40500, Text: "Vamos a San Diego."},
	}

	got := detectForeignCues(cues, "en")

	for i := 0; i <= 5; i++ {
		if lang, ok := got[i]; ok {
			t.Errorf("cue %d (%q) falsely flagged as %q", i, cues[i].Text, lang)
		}
	}
	for i := 6; i < len(cues); i++ {
		if lang := got[i]; lang != "es" {
			t.Errorf("cue %d (%q) = %q, want %q", i, cues[i].Text, lang, "es")
		}
	}
}

func TestDetectForeignCues_EnglishHomographsAndRepetitionsNotFlagged(t *testing.T) {
	cues := []subtext.Cue{
		{StartMS: 0, EndMS: 1500, Text: "Ha, ha, ha!"},
		{StartMS: 2000, EndMS: 3500, Text: "Go, go, go!"},
		{StartMS: 4000, EndMS: 5500, Text: "Um, um..."},
		{StartMS: 6000, EndMS: 7500, Text: "High heel."},
		{StartMS: 8000, EndMS: 9500, Text: "Never met."},
		{StartMS: 10000, EndMS: 11500, Text: "Trash bin."},
		{StartMS: 12000, EndMS: 14000, Text: "50 miles per hour."},
		{StartMS: 14500, EndMS: 16000, Text: "Dig deeper."},
		{StartMS: 16500, EndMS: 18000, Text: "One on one."},
		{StartMS: 18500, EndMS: 20000, Text: "Hey Dale!"},
	}

	got := detectForeignCues(cues, "en")
	for i, c := range cues {
		if lang, ok := got[i]; ok {
			t.Errorf("cue %d (%q) falsely flagged as %q", i, c.Text, lang)
		}
	}
}

func TestDetectForeignCues_ScriptsAndNonEnglishTargets(t *testing.T) {
	enCues := []subtext.Cue{
		{StartMS: 0, EndMS: 2000, Text: "Hello there, how are you?"},
		{StartMS: 2100, EndMS: 4000, Text: "Привет, как дела?"},
		{StartMS: 4100, EndMS: 6000, Text: "おはようございます"},
		{StartMS: 6100, EndMS: 8000, Text: "مرحبا كيف حالك"},
		{StartMS: 8100, EndMS: 10000, Text: "안녕하세요 반갑습니다"},
		{StartMS: 10100, EndMS: 12000, Text: "你好世界"},
	}
	got := detectForeignCues(enCues, "en")
	if _, ok := got[0]; ok {
		t.Fatalf("English cue flagged: %+v", got)
	}
	want := map[int]string{1: "ru", 2: "ja", 3: "ar", 4: "ko", 5: "zh"}
	for idx, w := range want {
		if got[idx] != w {
			t.Errorf("cue %d (%q) = %q, want %q", idx, enCues[idx].Text, got[idx], w)
		}
	}

	frCues := []subtext.Cue{
		{StartMS: 0, EndMS: 2000, Text: "Bonjour, comment allez-vous aujourd'hui ?"},
		{StartMS: 2100, EndMS: 4000, Text: "What are you doing here right now?"},
	}
	gotFR := detectForeignCues(frCues, "fr")
	if _, ok := gotFR[0]; ok {
		t.Errorf("French cue flagged in French target: %+v", gotFR)
	}
	if gotFR[1] != "en" {
		t.Errorf("English cue in French target = %q, want %q", gotFR[1], "en")
	}

	jaCues := []subtext.Cue{
		{StartMS: 0, EndMS: 2000, Text: "大丈夫ですか？"},
		{StartMS: 2100, EndMS: 4000, Text: "I think we should leave now."},
		{StartMS: 4100, EndMS: 6000, Text: "John Doe"},
	}
	gotJA := detectForeignCues(jaCues, "ja")
	if _, ok := gotJA[0]; ok {
		t.Errorf("Japanese cue flagged in Japanese target: %+v", gotJA)
	}
	if gotJA[1] != "en" {
		t.Errorf("English cue in Japanese target = %q, want %q", gotJA[1], "en")
	}
	if _, ok := gotJA[2]; ok {
		t.Errorf("2-word proper name falsely flagged in Japanese target: %+v", gotJA)
	}
}

func withCustomWhisperTranscript(t *testing.T, cues []transcribe.Cue) {
	t.Helper()
	type jsonCue struct {
		Offsets map[string]int64 `json:"offsets"`
		Text    string           `json:"text"`
	}
	payload := struct {
		Result        map[string]string `json:"result"`
		Transcription []jsonCue         `json:"transcription"`
	}{
		Result: map[string]string{"language": "en"},
	}
	for _, c := range cues {
		payload.Transcription = append(payload.Transcription, jsonCue{
			Offsets: map[string]int64{"from": c.StartMS, "to": c.EndMS},
			Text:    " " + c.Text,
		})
	}
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "transcript.json")
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jsonPath, b, 0o600); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(dir, "fake-whisper")
	script := `#!/bin/sh
out=""
dl=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    -dl) dl=1 ;;
    -of) out="$2"; shift ;;
  esac
  shift
done
if [ "$dl" -eq 1 ]; then
  echo "whisper_full_with_state: auto-detected language: en (p = 0.900000)" >&2
  exit 0
fi
cp "$FAKE_WHISPER_JSON" "${out}.json"
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_WHISPER_JSON", jsonPath)
	t.Setenv(transcribe.EnvWhisper, scriptPath)
}

func mixedSampleCues() []transcribe.Cue {
	return []transcribe.Cue{
		{StartMS: 0, EndMS: 2000, Text: "previously on 90 Day Fiancé."},
		{StartMS: 2500, EndMS: 5000, Text: "Devin, what's going on?"},
		{StartMS: 5500, EndMS: 8000, Text: "¿Estás listo?"},
		{StartMS: 8500, EndMS: 11000, Text: "Siento que este es el momento perfecto... de este"},
		{StartMS: 11100, EndMS: 12500, Text: "apartamento."},
		{StartMS: 13000, EndMS: 15500, Text: "Open the fucking curtain."},
	}
}

func TestTranscribe_TranslatesForeignCuesEndToEnd(t *testing.T) {
	f := newFix(t)
	ft := &faketranslator.OpenAI{Prefix: "EN: "}
	s, _ := withTranslator(t, f, ft)
	withTranscriber(t, f, s, "en,en,en", "eng")
	withCustomWhisperTranscript(t, mixedSampleCues())

	ctx := context.Background()
	wishAll(t, f, Want{Language: "en"})
	res, err := s.Transcribe(ctx, f.movieFile, true, domain.System)
	if err != nil {
		t.Fatalf("Transcribe failed: %v", err)
	}
	if res.Language != "en" || res.Lines != 6 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if ft.Calls == 0 {
		t.Fatal("expected translator to be called for Spanish cues")
	}
	if !strings.Contains(ft.System, "from Spanish to English") {
		t.Fatalf("expected Spanish->English system prompt, got %q", ft.System)
	}

	cues := readCues(t, f.abs(res.Path))
	if len(cues) != 6 {
		t.Fatalf("got %d cues, want 6", len(cues))
	}
	wantTexts := []string{
		"previously on 90 Day Fiancé.",
		"Devin, what's going on?",
		"EN: ¿Estás listo?",
		"EN: Siento que este es el\nmomento perfecto... de este",
		"EN: apartamento.",
		"Open the fucking curtain.",
	}
	src := mixedSampleCues()
	for i := range cues {
		if cues[i].Text != wantTexts[i] {
			t.Errorf("cue %d text = %q, want %q", i, cues[i].Text, wantTexts[i])
		}
		if cues[i].StartMS != src[i].StartMS || cues[i].EndMS != src[i].EndMS {
			t.Errorf("cue %d timing = (%d, %d), want (%d, %d)", i, cues[i].StartMS, cues[i].EndMS, src[i].StartMS, src[i].EndMS)
		}
	}
}

func TestTranscribe_NoTranslatorConfiguredKeepsOriginalCues(t *testing.T) {
	f := newFix(t)
	s := &Service{Store: f.st}
	withTranscriber(t, f, s, "en,en,en", "eng")
	withCustomWhisperTranscript(t, mixedSampleCues())

	ctx := context.Background()
	wishAll(t, f, Want{Language: "en"})
	res, err := s.Transcribe(ctx, f.movieFile, true, domain.System)
	if err != nil {
		t.Fatalf("Transcribe without translator failed: %v", err)
	}
	cues := readCues(t, f.abs(res.Path))
	src := mixedSampleCues()
	wantTexts := []string{
		"previously on 90 Day Fiancé.",
		"Devin, what's going on?",
		"¿Estás listo?",
		"Siento que este es el\nmomento perfecto... de este",
		"apartamento.",
		"Open the fucking curtain.",
	}
	if len(cues) != len(src) {
		t.Fatalf("got %d cues, want %d", len(cues), len(src))
	}
	for i := range cues {
		if cues[i].Text != wantTexts[i] {
			t.Errorf("cue %d text = %q, want %q", i, cues[i].Text, wantTexts[i])
		}
	}
}
