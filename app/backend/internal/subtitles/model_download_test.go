package subtitles

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate/builtin"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate/faketranslator"
)

// modelSite serves the bytes of one model and counts what it was asked.
type modelSite struct {
	body        []byte
	ignoreRange bool
	ranges      atomic.Int64
	stopAfter   atomic.Int64 // when > 0, the connection is cut after this many bytes
}

func (m *modelSite) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	from := 0
	if rg := r.Header.Get("Range"); rg != "" && !m.ignoreRange {
		m.ranges.Add(1)
		from, _ = strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(rg, "bytes="), "-"))
		w.Header().Set("Content-Range", "bytes "+strconv.Itoa(from)+"-"+strconv.Itoa(len(m.body)-1)+"/"+strconv.Itoa(len(m.body)))
		w.Header().Set("Content-Length", strconv.Itoa(len(m.body)-from))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.Header().Set("Content-Length", strconv.Itoa(len(m.body)))
	}
	rest := m.body[from:]
	if n := int(m.stopAfter.Load()); n > 0 && n < len(rest) {
		_, _ = w.Write(rest[:n])
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		panic(http.ErrAbortHandler) // the connection drops mid-file
	}
	_, _ = w.Write(rest)
}

// withCatalogue gives s the built-in runner and a catalogue of one small
// model served by a test site.
func withCatalogue(t *testing.T, s *Service) (*modelSite, builtin.Model) {
	t.Helper()
	withBuiltIn(t, s)
	body := []byte(strings.Repeat("model bytes 0123456789 ", 4000))
	sum := sha256.Sum256(body)
	site := &modelSite{body: body}
	srv := httptest.NewServer(site)
	t.Cleanup(srv.Close)
	m := builtin.Model{ID: "lab-small", Name: "Lab Small", URL: srv.URL + "/lab-small.gguf", SHA256: hex.EncodeToString(sum[:]), License: "Apache-2.0", Size: int64(len(body)), NeedBytes: 1 << 20}
	old := builtin.Catalogue
	builtin.Catalogue = []builtin.Model{m}
	t.Cleanup(func() { builtin.Catalogue = old })
	return site, m
}

func viewOf(t *testing.T, s *Service, id string) ModelView {
	t.Helper()
	for _, v := range s.Models(context.Background()) {
		if v.ID == id {
			return v
		}
	}
	t.Fatalf("no model %q listed", id)
	return ModelView{}
}

func noProgress(float64) {}

func TestDownloadChecksAndRenames(t *testing.T) {
	f := newFix(t)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	_, m := withCatalogue(t, s)
	if v := viewOf(t, s, m.ID); v.State != "missing" || v.Own || v.Name != "Lab Small" || v.Size != m.Size || !v.Fits {
		t.Fatalf("before: %+v", v)
	}
	var last float64
	if err := s.downloadModel(context.Background(), m.ID, func(p float64) { last = p }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.ModelsDir, m.ID+".gguf.part")); !os.IsNotExist(err) {
		t.Fatal("the .part file was left")
	}
	if v := viewOf(t, s, m.ID); v.State != "present" || last != 1 {
		t.Fatalf("after: %+v, last progress %v", v, last)
	}
	// The downloaded model can be chosen and makes the translator ready.
	tr, err := s.SaveTranslator(context.Background(), TranslatorInput{Kind: TranslatorBuiltIn, BuiltInModel: m.ID}, domain.System)
	if err != nil || !tr.Ready() || tr.Name() != "Lab Small" {
		t.Fatalf("chosen: %+v %v", tr, err)
	}
	if !viewOf(t, s, m.ID).Chosen {
		t.Fatal("not marked as chosen")
	}
}

func TestDownloadResumesAPartFile(t *testing.T) {
	f := newFix(t)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	site, m := withCatalogue(t, s)
	half := site.body[:len(site.body)/2]
	if err := os.WriteFile(filepath.Join(s.ModelsDir, m.ID+".gguf.part"), half, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.downloadModel(context.Background(), m.ID, noProgress); err != nil {
		t.Fatal(err)
	}
	if site.ranges.Load() != 1 {
		t.Fatalf("%d range requests, want 1", site.ranges.Load())
	}
	got, _ := os.ReadFile(filepath.Join(s.ModelsDir, m.ID+".gguf"))
	if string(got) != string(site.body) {
		t.Fatal("the resumed file differs from the model")
	}
}

func TestDownloadStartsOverWhenRangeIsIgnored(t *testing.T) {
	f := newFix(t)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	site, m := withCatalogue(t, s)
	site.ignoreRange = true
	_ = os.WriteFile(filepath.Join(s.ModelsDir, m.ID+".gguf.part"), site.body[:100], 0o644)
	if err := s.downloadModel(context.Background(), m.ID, noProgress); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(s.ModelsDir, m.ID+".gguf"))
	if string(got) != string(site.body) {
		t.Fatalf("got %d bytes, want %d", len(got), len(site.body))
	}
}

func TestDownloadBadChecksumLeavesNothing(t *testing.T) {
	f := newFix(t)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	site, m := withCatalogue(t, s)
	site.body = append([]byte("X"), site.body[1:]...) // same size, other bytes
	err := s.downloadModel(context.Background(), m.ID, noProgress)
	if err == nil || !strings.Contains(err.Error(), "Lab Small") {
		t.Fatalf("err %v", err)
	}
	left, _ := filepath.Glob(filepath.Join(s.ModelsDir, m.ID+"*"))
	if len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}
}

func TestPartFileIsNeverAModel(t *testing.T) {
	f := newFix(t)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	site, m := withCatalogue(t, s)
	_ = os.WriteFile(filepath.Join(s.ModelsDir, m.ID+".gguf.part"), site.body, 0o644)
	_ = os.WriteFile(filepath.Join(s.ModelsDir, "mine.gguf.part"), site.body, 0o644)
	if v := viewOf(t, s, m.ID); v.State != "missing" {
		t.Fatalf("a .part file counts as %q", v.State)
	}
	for _, v := range s.Models(context.Background()) {
		if strings.Contains(v.ID, ".part") {
			t.Fatalf("a .part file is listed: %+v", v)
		}
	}
	if _, err := s.modelFor(m.ID); !errors.Is(err, ErrNoModel) {
		t.Fatalf("modelFor: %v", err)
	}
	// A file of the wrong size under the model's name is not the model either.
	_ = os.WriteFile(filepath.Join(s.ModelsDir, m.ID+".gguf"), site.body[:10], 0o644)
	if _, err := s.modelFor(m.ID); !errors.Is(err, ErrNoModel) {
		t.Fatalf("a short file resolved: %v", err)
	}
	_, _ = s.SaveTranslator(context.Background(), TranslatorInput{Kind: TranslatorBuiltIn, BuiltInModel: m.ID}, domain.System)
	if s.Translator(context.Background()).Ready() {
		t.Fatal("ready on a half-downloaded model")
	}
}

func TestRestartMidDownload(t *testing.T) {
	f := newFix(t)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	site, m := withCatalogue(t, s)
	site.stopAfter.Store(int64(len(site.body) / 3))
	if err := s.downloadModel(context.Background(), m.ID, noProgress); err == nil {
		t.Fatal("a cut download reported success")
	}
	if v := viewOf(t, s, m.ID); v.State != "missing" {
		t.Fatalf("after the cut: %+v", v)
	}
	fi, err := os.Stat(filepath.Join(s.ModelsDir, m.ID+".gguf.part"))
	if err != nil || fi.Size() == 0 {
		t.Fatalf("the part file should be kept for the next try: %v", err)
	}
	site.stopAfter.Store(0)
	if err := s.downloadModel(context.Background(), m.ID, noProgress); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(s.ModelsDir, m.ID+".gguf"))
	if string(got) != string(site.body) || site.ranges.Load() != 1 {
		t.Fatalf("resumed file wrong (%d bytes) or not resumed (%d ranges)", len(got), site.ranges.Load())
	}
}

func TestCancelDownload(t *testing.T) {
	f := newFix(t)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	_, m := withCatalogue(t, s)
	ctx := context.Background()
	// Cancelled before it runs: the download ends at once and leaves nothing.
	if err := s.QueueModelDownload(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if v := viewOf(t, s, m.ID); v.State != "downloading" {
		t.Fatalf("queued: %+v", v)
	}
	if err := s.CancelModelDownload(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.runModelDownload(ctx, m.ID, noProgress); err != nil {
		t.Fatal(err)
	}
	left, _ := filepath.Glob(filepath.Join(s.ModelsDir, m.ID+"*"))
	if len(left) != 0 || viewOf(t, s, m.ID).State != "missing" {
		t.Fatalf("after cancel: %v %+v", left, viewOf(t, s, m.ID))
	}
	if err := s.QueueModelDownload(ctx, "nothing"); err == nil {
		t.Fatal("an unknown model was queued")
	}
	if err := s.QueueModelDownload(ctx, "file:lab.gguf"); err == nil {
		t.Fatal("an own file cannot be downloaded")
	}
}

func TestDeleteChosenModelRefused(t *testing.T) {
	f := newFix(t)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	_, m := withCatalogue(t, s)
	ctx := context.Background()
	if err := s.downloadModel(ctx, m.ID, noProgress); err != nil {
		t.Fatal(err)
	}
	before := s.Translator(ctx)
	if _, err := s.SaveTranslator(ctx, TranslatorInput{Kind: TranslatorBuiltIn, BuiltInModel: m.ID}, domain.System); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteModel(ctx, m.ID); !errors.Is(err, ErrModelInUse) {
		t.Fatalf("err %v", err)
	}
	if _, err := s.SaveTranslator(ctx, TranslatorInput{Kind: TranslatorOpenAI, URL: before.URL, Model: before.Model, BuiltInModel: m.ID}, domain.System); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteModel(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if viewOf(t, s, m.ID).State != "missing" {
		t.Fatal("still there after delete")
	}
	// Own files can be deleted too, and only inside the models folder.
	if err := s.DeleteModel(ctx, "file:lab.gguf"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteModel(ctx, "file:../holyarrs.db"); err == nil {
		t.Fatal("a path outside the models folder was accepted")
	}
}

func TestModelsListsOwnFiles(t *testing.T) {
	f := newFix(t)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	_, m := withCatalogue(t, s)
	vs := s.Models(context.Background())
	if len(vs) != 2 || vs[0].ID != m.ID || vs[1].ID != "file:lab.gguf" || !vs[1].Own || vs[1].State != "present" || vs[1].Name != "lab" {
		t.Fatalf("models %+v", vs)
	}
}

// The catalogue says how a model is asked, and that reaches the client.
func TestCatalogueFormReachesTheClient(t *testing.T) {
	if m, ok := builtin.ByID("hy-mt2-1.8b-q8"); !ok || m.Form != builtin.FormHyMT {
		t.Fatalf("Hy-MT2 in the catalogue: %+v %v", m, ok)
	}
	f := newFix(t)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	_, m := withCatalogue(t, s)
	builtin.Catalogue[0].Form = builtin.FormHyMT
	ctx := context.Background()
	if err := s.downloadModel(ctx, m.ID, noProgress); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveTranslator(ctx, TranslatorInput{Kind: TranslatorBuiltIn, BuiltInModel: m.ID}, domain.System); err != nil {
		t.Fatal(err)
	}
	c, _, err := s.translatorClient(ctx)
	if err != nil || c.(*builtin.Client).Form != builtin.FormHyMT {
		t.Fatalf("client %+v %v", c, err)
	}
}

// stallSite sends the start of a model and then nothing more.
type stallSite struct{ body []byte }

func (m *stallSite) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Length", strconv.Itoa(len(m.body)))
	_, _ = w.Write(m.body[:1000])
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	<-r.Context().Done()
}

// A server that stops sending is given up after a while, so a download can
// never hang for ever; what came stays for the next try.
func TestDownloadGivesUpOnAStalledServer(t *testing.T) {
	f := newFix(t)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	site, m := withCatalogue(t, s)
	srv := httptest.NewServer(&stallSite{body: site.body})
	t.Cleanup(srv.Close)
	builtin.Catalogue[0].URL = srv.URL + "/lab-small.gguf"
	old := modelStall
	modelStall = 200 * time.Millisecond
	defer func() { modelStall = old }()
	done := make(chan error, 1)
	go func() { done <- s.downloadModel(context.Background(), m.ID, noProgress) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a stalled download reported success")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the download hung on a server that stopped sending")
	}
	if fi, err := os.Stat(filepath.Join(s.ModelsDir, m.ID+".gguf.part")); err != nil || fi.Size() == 0 {
		t.Fatalf("what came should stay for the next try: %v", err)
	}
}

// A catalogue model's file cannot be named as an own file: that would get
// round the in-use check and load the model without its own prompt form.
func TestOwnIDCannotNameACatalogueFile(t *testing.T) {
	f := newFix(t)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	_, m := withCatalogue(t, s)
	ctx := context.Background()
	if err := s.downloadModel(ctx, m.ID, noProgress); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveTranslator(ctx, TranslatorInput{Kind: TranslatorBuiltIn, BuiltInModel: m.ID}, domain.System); err != nil {
		t.Fatal(err)
	}
	for _, alias := range []string{"file:" + m.ID + ".gguf", "file:" + strings.ToUpper(m.ID) + ".GGUF"} {
		if validModelID(alias) {
			t.Fatalf("%q is taken for an own file", alias)
		}
		if err := s.DeleteModel(ctx, alias); err == nil {
			t.Fatalf("%q deleted the model in use", alias)
		}
		if _, err := s.SaveTranslator(ctx, TranslatorInput{Kind: TranslatorBuiltIn, BuiltInModel: alias}, domain.System); err == nil {
			t.Fatalf("%q was saved as the model", alias)
		}
	}
	if _, err := s.modelFor(m.ID); err != nil {
		t.Fatalf("the model in use is gone: %v", err)
	}
}
