package subtitles

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/jobs"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/transcribe"
)

// route: GET /api/v1/subtitles/waiting
// route: POST /api/v1/subtitles/jobs/{id}/bump
// route: POST /api/v1/subtitles/jobs/{id}/skip
// route: POST /api/v1/files/{id}/subtitles/skip
func TestWaitingBumpAndSkipRoutes(t *testing.T) {
	f := newFix(t)
	ctx := context.Background()
	q := jobs.New(f.st, jobs.Options{Workers: 1, PollInterval: time.Hour})
	s := &Service{Store: f.st}
	s.Register(q)
	m := http.NewServeMux()
	NewHandlers(f.st, nil, s).Routes(m)

	// Give movieFile two wishes (en, es) and ep1File one wish (fr).
	ts := database.FormatTime(now())
	err := f.st.Write(ctx, func(tx *sql.Tx) error {
		for _, w := range []struct {
			fid  int64
			lang string
		}{
			{f.movieFile, "en"},
			{f.movieFile, "es"},
			{f.ep1File, "fr"},
		} {
			if _, err := tx.Exec(`INSERT INTO subtitles (created_at, created_by, updated_at, updated_by, file_id, language, place, origin, state)
				VALUES (?, ?, ?, ?, ?, ?, 'wish', 'fetched', 'wanted')`, ts, sysActor, ts, sysActor, w.fid, w.lang); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Queue a search on movieFile, a transcribe on movieFile, and a translate (fr) on ep1File.
	if err := s.Queue(ctx, f.movieFile, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.QueueTranscribe(ctx, f.movieFile, false); err != nil {
		t.Fatal(err)
	}
	if err := s.QueueTranslate(ctx, f.ep1File, TranslateRequest{Language: "fr", Forced: false}); err != nil {
		t.Fatal(err)
	}

	// GET /api/v1/subtitles/waiting returns all 3 jobs in FIFO order with file metadata.
	code, body := call(m, "GET", "/api/v1/subtitles/waiting", "")
	if code != http.StatusOK {
		t.Fatalf("GET waiting %d: %s", code, body)
	}
	var waitResp struct {
		Jobs  []WaitingJob `json:"jobs"`
		Total int          `json:"total"`
	}
	if err := json.Unmarshal([]byte(body), &waitResp); err != nil {
		t.Fatal(err)
	}
	if waitResp.Total != 3 || len(waitResp.Jobs) != 3 {
		t.Fatalf("waiting got %+v, want 3", waitResp)
	}
	if waitResp.Jobs[0].Position != 1 || waitResp.Jobs[1].Position != 2 || waitResp.Jobs[2].Position != 3 {
		t.Fatalf("unexpected positions: %+v", waitResp.Jobs)
	}
	if waitResp.Jobs[2].Name != "Quiet Harbour" || waitResp.Jobs[2].MediaKind != "episode" || waitResp.Jobs[2].Language != "fr" || waitResp.Jobs[2].RelativePath == "" {
		t.Fatalf("missing episode metadata on waiting job: %+v", waitResp.Jobs[2])
	}

	firstID := waitResp.Jobs[0].ID
	thirdID := waitResp.Jobs[2].ID

	// Bump thirdID (translate on Quiet Harbour) to position 1 with priority='now'.
	code, body = call(m, "POST", "/api/v1/subtitles/jobs/"+itoa(thirdID)+"/bump", "")
	if code != http.StatusOK {
		t.Fatalf("POST bump %d: %s", code, body)
	}
	var bumpResp struct {
		ID       int64  `json:"id"`
		Priority string `json:"priority"`
		State    string `json:"state"`
	}
	if err := json.Unmarshal([]byte(body), &bumpResp); err != nil {
		t.Fatal(err)
	}
	if bumpResp.ID != thirdID || bumpResp.Priority != "now" || bumpResp.State != "queued" {
		t.Fatalf("unexpected bump response: %+v", bumpResp)
	}

	// Non-subtitle job bump returns 404.
	otherID := insertTestJob(t, f.st, "library_scan", "queued", "", nil)
	if c, _ := call(m, "POST", "/api/v1/subtitles/jobs/"+itoa(otherID)+"/bump", ""); c != http.StatusNotFound {
		t.Fatalf("bump non-subtitle job: %d, want 404", c)
	}

	// Filtered q search preserves actual 1-based queue position (firstID is now position 2).
	code, body = call(m, "GET", "/api/v1/subtitles/waiting?q=The+Lantern&kind="+KindSearch, "")
	if code != http.StatusOK {
		t.Fatalf("GET waiting filtered %d: %s", code, body)
	}
	var filtResp struct {
		Jobs  []WaitingJob `json:"jobs"`
		Total int          `json:"total"`
	}
	if err := json.Unmarshal([]byte(body), &filtResp); err != nil {
		t.Fatal(err)
	}
	if filtResp.Total != 1 || filtResp.Jobs[0].ID != firstID || filtResp.Jobs[0].Position != 2 {
		t.Fatalf("filtered waiting got %+v, want ID %d at position 2", filtResp, firstID)
	}

	// Pagination works cleanly.
	code, body = call(m, "GET", "/api/v1/subtitles/waiting?limit=2&offset=1", "")
	if code != http.StatusOK {
		t.Fatalf("GET waiting page %d: %s", code, body)
	}
	if err := json.Unmarshal([]byte(body), &filtResp); err != nil {
		t.Fatal(err)
	}
	if filtResp.Total != 3 || len(filtResp.Jobs) != 2 || filtResp.Jobs[0].Position != 2 || filtResp.Jobs[1].Position != 3 {
		t.Fatalf("paginated waiting got %+v", filtResp)
	}

	// Skip thirdID (translate fr on ep1File): drops job without leaving a cancelled row in /failed and marks fr wish skipped.
	code, body = call(m, "POST", "/api/v1/subtitles/jobs/"+itoa(thirdID)+"/skip", "")
	if code != http.StatusOK {
		t.Fatalf("POST skip job %d: %s", code, body)
	}
	var skipJobResp struct {
		ID      int64 `json:"id"`
		Skipped bool  `json:"skipped"`
		FileID  int64 `json:"file_id"`
	}
	_ = json.Unmarshal([]byte(body), &skipJobResp)
	if !skipJobResp.Skipped || skipJobResp.FileID != f.ep1File {
		t.Fatalf("unexpected skip job response: %+v", skipJobResp)
	}

	code, body = call(m, "GET", "/api/v1/subtitles/failed", "")
	if code != http.StatusOK || !strings.Contains(body, `"total":0`) {
		t.Fatalf("expected 0 failed/cancelled jobs after DropQueued skip, got %d %s", code, body)
	}

	// POST /api/v1/files/{id}/subtitles/skip with language=es skips only es on movieFile and keeps search job.
	code, body = call(m, "POST", "/api/v1/files/"+itoa(f.movieFile)+"/subtitles/skip", `{"language":"es"}`)
	if code != http.StatusOK || !strings.Contains(body, `"count":1`) {
		t.Fatalf("skip file es: %d %s", code, body)
	}
	code, body = call(m, "GET", "/api/v1/subtitles/waiting", "")
	_ = json.Unmarshal([]byte(body), &waitResp)
	if waitResp.Total != 2 {
		t.Fatalf("expected 2 movie jobs remaining while en is still wanted, got %+v", waitResp)
	}

	// POST /api/v1/files/{id}/subtitles/skip with empty body skips remaining en wish and drops both movie jobs.
	code, body = call(m, "POST", "/api/v1/files/"+itoa(f.movieFile)+"/subtitles/skip", "")
	if code != http.StatusOK || !strings.Contains(body, `"count":1`) {
		t.Fatalf("skip file all: %d %s", code, body)
	}
	code, body = call(m, "GET", "/api/v1/subtitles/waiting", "")
	_ = json.Unmarshal([]byte(body), &waitResp)
	if waitResp.Total != 0 {
		t.Fatalf("expected 0 waiting jobs after skipping all movie wishes, got %+v", waitResp)
	}

	// Overview reflects skipped_count=3 and missing=0.
	ov, err := s.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ov.SkippedCount != 3 || ov.Missing != 0 {
		t.Fatalf("overview skipped=%d missing=%d, want 3 and 0", ov.SkippedCount, ov.Missing)
	}
}

// route: GET /api/v1/subtitles/history
func TestHistoryEndpointAndFilters(t *testing.T) {
	f := newFix(t)
	s := &Service{Store: f.st}
	m := http.NewServeMux()
	NewHandlers(f.st, nil, s).Routes(m)

	base := now()
	savedRow(t, f, f.movieFile, "en", "The Lantern (2010)/The Lantern (2010) 1080p BluRay.en.srt", "OpenSubtitles", nil, false, base.Add(-4*time.Hour))
	savedRow(t, f, f.ep1File, "fr", "Quiet Harbour (2019)/Season 01/Quiet Harbour - S01E02.fr.srt", "Built in", MadeTranslated, false, base.Add(-3*time.Hour))
	savedRow(t, f, f.ep2File, "de", "Quiet Harbour (2019)/Season 01/Quiet Harbour - S01E03.de.srt", "Whisper", "ai_translated", true, base.Add(-1*time.Hour))

	// Insert one skipped wish at base-2h (between fr done and de speech done).
	err := f.st.Write(context.Background(), func(tx *sql.Tx) error {
		ts := database.FormatTime(base.Add(-2 * time.Hour))
		_, err := tx.Exec(`INSERT INTO subtitles (created_at, created_by, updated_at, updated_by, file_id, language, place, origin, state)
			VALUES (?, ?, ?, ?, ?, 'es', 'wish', 'fetched', 'skipped')`, ts, sysActor, ts, sysActor, f.movieFile)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	// state=all returns all 4 ordered newest-first: de speech (-1h), es skipped (-2h), fr translated (-3h), en downloaded (-4h).
	code, body := call(m, "GET", "/api/v1/subtitles/history?state=all", "")
	if code != http.StatusOK {
		t.Fatalf("GET history all: %d %s", code, body)
	}
	var res HistoryResult
	if err := json.Unmarshal([]byte(body), &res); err != nil {
		t.Fatal(err)
	}
	if res.Total != 4 || len(res.Items) != 4 || res.DoneCount != 3 || res.SkippedCount != 1 ||
		res.DoneCounts.Downloaded != 1 || res.DoneCounts.Translated != 1 || res.DoneCounts.Speech != 1 {
		t.Fatalf("history summary mismatch: %+v", res)
	}
	wantOrder := []struct {
		state, how, lang, src string
	}{
		{"done", "speech", "de", "Whisper"},
		{"skipped", "skipped", "es", ""},
		{"done", "translated", "fr", "Built in"},
		{"done", "downloaded", "en", "OpenSubtitles"},
	}
	for i, w := range wantOrder {
		got := res.Items[i]
		if got.State != w.state || got.How != w.how || got.Language != w.lang || got.Source != w.src {
			t.Fatalf("item[%d] = %+v, want %+v", i, got, w)
		}
	}

	// state=skipped returns only the skipped item.
	code, body = call(m, "GET", "/api/v1/subtitles/history?state=skipped", "")
	if code != http.StatusOK {
		t.Fatalf("GET history skipped: %d %s", code, body)
	}
	_ = json.Unmarshal([]byte(body), &res)
	if res.Total != 1 || len(res.Items) != 1 || res.Items[0].State != "skipped" || res.Items[0].Language != "es" {
		t.Fatalf("history skipped mismatch: %+v", res)
	}

	// q search matches source, title, and language name (e.g. "Spanish").
	for _, tc := range []struct {
		q     string
		count int
		lang  string
	}{
		{"OpenSubtitles", 1, "en"},
		{"Spanish", 1, "es"},
		{"Quiet Harbour", 2, "de"},
	} {
		code, body = call(m, "GET", "/api/v1/subtitles/history?q="+strings.ReplaceAll(tc.q, " ", "+"), "")
		if code != http.StatusOK {
			t.Fatalf("GET history q=%s: %d %s", tc.q, code, body)
		}
		_ = json.Unmarshal([]byte(body), &res)
		if res.Total != tc.count || res.Items[0].Language != tc.lang {
			t.Fatalf("q=%s got total=%d items=%+v", tc.q, res.Total, res.Items)
		}
	}

	// Invalid state returns 400.
	if code, _ := call(m, "GET", "/api/v1/subtitles/history?state=bogus", ""); code != http.StatusBadRequest {
		t.Fatalf("bad state code=%d, want 400", code)
	}
}

func TestLookPreservesSkippedSkipsNoOpWriteAndManualSearchUnskips(t *testing.T) {
	f := newFix(t)
	ctx := context.Background()
	clock := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	oldNow := now
	now = func() time.Time { return clock }
	defer func() { now = oldNow }()

	q := jobs.New(f.st, jobs.Options{Workers: 1, PollInterval: time.Hour})
	s := &Service{Store: f.st}
	s.Register(q)
	m := http.NewServeMux()
	NewHandlers(f.st, nil, s).Routes(m)

	if err := SetWish(ctx, f.st, f.movie, 0, 0, []Want{{Language: "en"}, {Language: "es"}}, false, domain.System); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Look(ctx, f.movieFile, domain.System); err != nil {
		t.Fatal(err)
	}

	// Skip Spanish on movieFile.
	if _, err := s.SkipFile(ctx, f.movieFile, "es", false, domain.System); err != nil {
		t.Fatal(err)
	}

	// Advance clock and run Look again with nothing changed on disk or in wish.
	var firstLookedAt string
	if err := f.st.Read().QueryRow("SELECT looked_at FROM subtitle_looks WHERE file_id = ?", f.movieFile).Scan(&firstLookedAt); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(2 * time.Hour)
	if _, err := s.Look(ctx, f.movieFile, domain.System); err != nil {
		t.Fatal(err)
	}

	// Unchanged Look skips the write transaction so looked_at stays unchanged and 'es' stays skipped.
	var secondLookedAt string
	if err := f.st.Read().QueryRow("SELECT looked_at FROM subtitle_looks WHERE file_id = ?", f.movieFile).Scan(&secondLookedAt); err != nil {
		t.Fatal(err)
	}
	if secondLookedAt != firstLookedAt {
		t.Fatalf("unchanged Look wrote subtitle_looks: got %s, want %s", secondLookedAt, firstLookedAt)
	}
	var esState string
	if err := f.st.Read().QueryRow("SELECT state FROM subtitles WHERE file_id = ? AND place = 'wish' AND language = 'es'", f.movieFile).Scan(&esState); err != nil || esState != "skipped" {
		t.Fatalf("es state after rescan = %q (%v), want skipped", esState, err)
	}

	// Even when another wish changes (forcing a write tx), skipped 'es' stays skipped while 'es' remains wanted.
	if err := SetWish(ctx, f.st, f.movie, 0, 0, []Want{{Language: "en"}, {Language: "es"}, {Language: "fr"}}, false, domain.System); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Look(ctx, f.movieFile, domain.System); err != nil {
		t.Fatal(err)
	}
	if err := f.st.Read().QueryRow("SELECT state FROM subtitles WHERE file_id = ? AND place = 'wish' AND language = 'es'", f.movieFile).Scan(&esState); err != nil || esState != "skipped" {
		t.Fatalf("es state after wish addition = %q (%v), want skipped", esState, err)
	}

	// Manual search via POST /api/v1/subtitles/search un-skips 'es' back to 'wanted'.
	code, body := call(m, "POST", "/api/v1/subtitles/search", `{"file_id":`+itoa(f.movieFile)+`}`)
	if code != http.StatusAccepted {
		t.Fatalf("manual search: %d %s", code, body)
	}
	if err := f.st.Read().QueryRow("SELECT state FROM subtitles WHERE file_id = ? AND place = 'wish' AND language = 'es'", f.movieFile).Scan(&esState); err != nil || esState != "wanted" {
		t.Fatalf("es state after manual search = %q (%v), want wanted", esState, err)
	}
}

func TestAudioTracksResolutionAvoidsFFprobe(t *testing.T) {
	f := newFix(t)
	ctx := context.Background()
	s := &Service{Store: f.st}

	// Point ffprobe to a missing path so any ffprobe spawn would fail.
	progs := transcribe.Programs{FFprobe: filepath.Join(t.TempDir(), "missing-ffprobe")}

	// 1. Cached MediaInfo with single audio track and runtime_seconds avoids both container read and ffprobe.
	err := f.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE files SET media_info = '{"audio_tracks":1,"audio_unknown":false,"audio_languages":["fr"],"runtime_seconds":1800}' WHERE id = ?`, f.movieFile)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	fc, err := s.load(ctx, f.movieFile)
	if err != nil {
		t.Fatal(err)
	}
	tracks, err := audioTracksForFile(ctx, progs, fc)
	if err != nil || len(tracks) != 1 || tracks[0].Index != 0 || tracks[0].Language != "fr" || tracks[0].Seconds != 1800 {
		t.Fatalf("MediaInfo resolution got %+v, err=%v", tracks, err)
	}

	// 2. Empty MediaInfo falls back to pure-Go container header reading when audio tracks + duration are present.
	err = f.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE files SET media_info = '{}' WHERE id = ?`, f.ep1File)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	mkvBytes := testAudioMKVBytes(90.0, "eng", "jpn")
	ep1Path := filepath.Join(f.lib, filepath.FromSlash("Quiet Harbour (2019)/Season 01/Quiet Harbour - S01E02.mkv"))
	if err := os.WriteFile(ep1Path, mkvBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	fcEp, err := s.load(ctx, f.ep1File)
	if err != nil {
		t.Fatal(err)
	}
	tracks, err = audioTracksForFile(ctx, progs, fcEp)
	if err != nil || len(tracks) != 2 || tracks[0].Index != 0 || tracks[0].Language != "en" || tracks[1].Index != 1 || tracks[1].Language != "ja" || tracks[0].Seconds < 89 {
		t.Fatalf("container resolution got %+v, err=%v", tracks, err)
	}
}

func testAudioMKVBytes(durationSec float64, langs ...string) []byte {
	el := func(id []byte, payload []byte) []byte {
		var sz []byte
		n := len(payload)
		if n < 0x7f {
			sz = []byte{byte(0x80 | n)}
		} else {
			sz = []byte{byte(0x40 | (n >> 8)), byte(n)}
		}
		out := make([]byte, 0, len(id)+len(sz)+len(payload))
		out = append(out, id...)
		out = append(out, sz...)
		out = append(out, payload...)
		return out
	}
	header := el([]byte{0x1a, 0x45, 0xdf, 0xa3}, el([]byte{0x42, 0x82}, []byte("matroska")))
	var durBuf [8]byte
	binary.BigEndian.PutUint64(durBuf[:], math.Float64bits(durationSec*1000))
	info := el([]byte{0x15, 0x49, 0xa9, 0x66}, bytes.Join([][]byte{
		el([]byte{0x2a, 0xd7, 0xb1}, []byte{0x0f, 0x42, 0x40}), // 1,000,000 ns timecode scale
		el([]byte{0x44, 0x89}, durBuf[:]),
	}, nil))
	var trackEntries [][]byte
	for _, l := range langs {
		entry := bytes.Join([][]byte{
			el([]byte{0x83}, []byte{2}), // audio
			el([]byte{0x86}, []byte("A_AAC")),
			el([]byte{0x22, 0xb5, 0x9c}, []byte(l)),
		}, nil)
		trackEntries = append(trackEntries, el([]byte{0xae}, entry))
	}
	tracks := el([]byte{0x16, 0x54, 0xae, 0x6b}, bytes.Join(trackEntries, nil))
	segment := el([]byte{0x18, 0x53, 0x80, 0x67}, bytes.Join([][]byte{info, tracks}, nil))
	return append(header, segment...)
}
