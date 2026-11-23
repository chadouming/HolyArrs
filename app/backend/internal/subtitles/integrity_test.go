package subtitles

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/jobs"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/sites/fakesite"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/subtext"
)

func buildTimedSRT(cues int, lastStartSec int) string {
	var b strings.Builder
	step := max(lastStartSec/max(cues, 1), 1)
	for i := range cues {
		sec := (i + 1) * step
		if i == cues-1 {
			sec = lastStartSec
		}
		fmtMS := func(s int) string {
			return fmt.Sprintf("%02d:%02d:%02d,000", s/3600, (s/60)%60, s%60)
		}
		fmt.Fprintf(&b, "%d\n%s --> %s\nLine %d\n\n", i+1, fmtMS(sec), fmtMS(sec+2), i+1)
	}
	return b.String()
}

func setFileRuntimeSeconds(t *testing.T, st *database.Store, fileID int64, sec int) {
	t.Helper()
	err := st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE files SET media_info = ? WHERE id = ?`, fmt.Sprintf(`{"runtime_seconds":%d}`, sec), fileID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func writeBesideSub(t *testing.T, f *fix, videoRel, lang, content string) string {
	t.Helper()
	rel := SubtitleName(videoRel, lang, false, false, "hi", "srt")
	p := filepath.Join(f.lib, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return rel
}

func insertBesideRow(t *testing.T, st *database.Store, fileID int64, lang, rel, origin string, siteName, siteKey *string) int64 {
	t.Helper()
	ts := database.FormatTime(now())
	var id int64
	err := st.Write(context.Background(), func(tx *sql.Tx) error {
		r, err := tx.Exec(`INSERT INTO subtitles (created_at, created_by, updated_at, updated_by, file_id, language, place, relative_path, origin, site_name, site_key, state)
			VALUES (?, ?, ?, ?, ?, ?, 'beside', ?, ?, ?, ?, 'present')`,
			ts, sysActor, ts, sysActor, fileID, lang, rel, origin, siteName, siteKey)
		if err != nil {
			return err
		}
		id, _ = r.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func getSubRow(t *testing.T, st *database.Store, id int64) Subtitle {
	t.Helper()
	var sub Subtitle
	if err := database.Get(context.Background(), st.Read(), "subtitles", id, &sub); err != nil {
		t.Fatal(err)
	}
	return sub
}

// VerifyAll classifies valid, truncated, and unreadable beside subtitles, and
// the integrity HTTP routes support verifying, keeping, and replacing flagged rows.
// route: GET /api/v1/subtitles/integrity
// route: POST /api/v1/subtitles/verify
// route: POST /api/v1/subtitles/{id}/keep
// route: POST /api/v1/subtitles/{id}/replace
func TestIntegrity_VerifyKeepReplaceAndRoutes(t *testing.T) {
	f := newFix(t)
	ctx := context.Background()
	for _, fid := range []int64{f.movieFile, f.ep1File, f.ep2File} {
		setFileRuntimeSeconds(t, f.st, fid, 3600)
	}
	wishAll(t, f, Want{Language: "en"})

	movieRel := "The Lantern (2010)/The Lantern (2010) 1080p BluRay.mkv"
	ep1Rel := "Quiet Harbour (2019)/Season 01/Quiet Harbour - S01E02.mkv"
	ep2Rel := "Quiet Harbour (2019)/Season 01/Quiet Harbour - S01E03.mkv"

	// Valid beside subtitle on ep1, truncated site-fetched subtitle on movie, unreadable on ep2.
	validRel := writeBesideSub(t, f, ep1Rel, "en", buildTimedSRT(15, 3300))
	truncRel := writeBesideSub(t, f, movieRel, "en", buildTimedSRT(15, 300))
	unreadableRel := writeBesideSub(t, f, ep2Rel, "en", "this is not a subtitle")

	validID := insertBesideRow(t, f.st, f.ep1File, "en", validRel, "found", nil, nil)
	siteName, siteKey := "fake", "trunc-site-key"
	truncID := insertBesideRow(t, f.st, f.movieFile, "en", truncRel, "fetched", &siteName, &siteKey)
	unreadableID := insertBesideRow(t, f.st, f.ep2File, "en", unreadableRel, "found", nil, nil)

	var enqueued []string
	s := &Service{
		Store: f.st,
		Enqueue: func(_ context.Context, kind string, subj *domain.Subject, _ map[string]any, _ string) error {
			if subj != nil {
				enqueued = append(enqueued, kind+":"+itoa(subj.ID))
			} else {
				enqueued = append(enqueued, kind)
			}
			return nil
		},
	}

	if err := s.VerifyAll(ctx, &jobs.Run{}); err != nil {
		t.Fatal(err)
	}

	validRow := getSubRow(t, f.st, validID)
	if validRow.IntegrityState == nil || *validRow.IntegrityState != "ok" {
		t.Fatalf("valid row state = %v, want ok", validRow.IntegrityState)
	}
	truncRow := getSubRow(t, f.st, truncID)
	if truncRow.IntegrityState == nil || *truncRow.IntegrityState != "bad" || truncRow.IntegrityIssue == nil || *truncRow.IntegrityIssue != string(subtext.IssueTruncated) {
		t.Fatalf("truncated row = state:%v issue:%v", truncRow.IntegrityState, truncRow.IntegrityIssue)
	}
	unreadRow := getSubRow(t, f.st, unreadableID)
	if unreadRow.IntegrityState == nil || *unreadRow.IntegrityState != "bad" || unreadRow.IntegrityIssue == nil || *unreadRow.IntegrityIssue != string(subtext.IssueUnreadable) {
		t.Fatalf("unreadable row = state:%v issue:%v", unreadRow.IntegrityState, unreadRow.IntegrityIssue)
	}

	m := http.NewServeMux()
	NewHandlers(f.st, nil, s).Routes(m)

	// GET /api/v1/subtitles/integrity lists summary and both bad issues.
	code, body := call(m, "GET", "/api/v1/subtitles/integrity", "")
	if code != http.StatusOK {
		t.Fatalf("GET integrity: %d %s", code, body)
	}
	var listRes struct {
		Summary IntegritySummary `json:"summary"`
		Issues  []IntegrityRow   `json:"issues"`
		Total   int              `json:"total"`
	}
	if err := json.Unmarshal([]byte(body), &listRes); err != nil {
		t.Fatal(err)
	}
	if listRes.Summary.OK != 1 || listRes.Summary.Bad != 2 || listRes.Total != 2 || len(listRes.Issues) != 2 {
		t.Fatalf("integrity response = %+v", listRes)
	}

	// POST /api/v1/subtitles/verify queues a verify job.
	code, body = call(m, "POST", "/api/v1/subtitles/verify", "")
	if code != http.StatusAccepted || !strings.Contains(body, `"queued":true`) {
		t.Fatalf("POST verify: %d %s", code, body)
	}
	if len(enqueued) != 1 || enqueued[0] != KindVerify {
		t.Fatalf("enqueued after verify = %v", enqueued)
	}

	// POST /api/v1/subtitles/{id}/keep accepts the unreadable subtitle and skips it on next verify.
	code, body = call(m, "POST", "/api/v1/subtitles/"+itoa(unreadableID)+"/keep", "")
	if code != http.StatusOK || !strings.Contains(body, `"kept":true`) {
		t.Fatalf("POST keep: %d %s", code, body)
	}
	keptRow := getSubRow(t, f.st, unreadableID)
	if keptRow.IntegrityState == nil || *keptRow.IntegrityState != "accepted" {
		t.Fatalf("kept row state = %v, want accepted", keptRow.IntegrityState)
	}
	if err := s.VerifyAll(ctx, &jobs.Run{}); err != nil {
		t.Fatal(err)
	}
	keptAfter := getSubRow(t, f.st, unreadableID)
	if keptAfter.IntegrityState == nil || *keptAfter.IntegrityState != "accepted" {
		t.Fatalf("accepted row was re-flagged on verify: %v", keptAfter.IntegrityState)
	}
	sumAfterKeep, err := s.IntegritySummary(ctx)
	if err != nil || sumAfterKeep.Accepted != 1 || sumAfterKeep.Bad != 1 || sumAfterKeep.OK != 1 {
		t.Fatalf("summary after keep = %+v, err=%v", sumAfterKeep, err)
	}

	// POST /api/v1/subtitles/{id}/replace moves bad file to holding folder, marks replaced,
	// turns down the site candidate, recreates the wish, and queues a search.
	enqueued = nil
	code, body = call(m, "POST", "/api/v1/subtitles/"+itoa(truncID)+"/replace", "")
	if code != http.StatusOK || !strings.Contains(body, `"replaced":true`) {
		t.Fatalf("POST replace: %d %s", code, body)
	}
	if _, err := os.Stat(f.abs(truncRel)); !os.IsNotExist(err) {
		t.Fatalf("bad subtitle file still present at %s", truncRel)
	}
	held, _ := filepath.Glob(filepath.Join(f.lib, ".holyarrs-replaced", "*", "The Lantern (2010)", "*.en.srt"))
	if len(held) != 1 {
		t.Fatalf("expected 1 held file in .holyarrs-replaced, got %v", held)
	}
	replacedRow := getSubRow(t, f.st, truncID)
	if replacedRow.State != "replaced" {
		t.Fatalf("replaced row state = %s, want replaced", replacedRow.State)
	}
	var turnedDownCount int
	if err := f.st.Read().QueryRow(`SELECT count(*) FROM subtitle_turned_down WHERE file_id = ? AND site_name = ? AND site_key = ?`,
		f.movieFile, siteName, siteKey).Scan(&turnedDownCount); err != nil || turnedDownCount != 1 {
		t.Fatalf("turned_down count = %d, err=%v", turnedDownCount, err)
	}
	var wishCount int
	if err := f.st.Read().QueryRow(`SELECT count(*) FROM subtitles WHERE file_id = ? AND place = 'wish' AND language = 'en' AND state = 'wanted'`,
		f.movieFile).Scan(&wishCount); err != nil || wishCount != 1 {
		t.Fatalf("wish count after replace = %d, err=%v", wishCount, err)
	}
	if len(enqueued) != 1 || enqueued[0] != KindSearch+":"+itoa(f.movieFile) {
		t.Fatalf("enqueued after replace = %v", enqueued)
	}

	if code, _ := call(m, "POST", "/api/v1/subtitles/999999/keep", ""); code != http.StatusNotFound {
		t.Fatalf("keep missing subtitle: %d, want 404", code)
	}
	if code, _ := call(m, "POST", "/api/v1/subtitles/999999/replace", ""); code != http.StatusNotFound {
		t.Fatalf("replace missing subtitle: %d, want 404", code)
	}
}

// Automatic Search rejects a truncated download candidate and falls back to the next valid one.
func TestSearch_RejectsTruncatedCandidateAndFallsBack(t *testing.T) {
	f := newFix(t)
	setFileRuntimeSeconds(t, f.st, f.movieFile, 3600)

	site := &fakesite.Site{
		Subs: []fakesite.Sub{
			{
				ID:        "trunc-1",
				Kind:      "movie",
				Language:  "en",
				Title:     "The Lantern",
				Year:      2010,
				Release:   "The.Lantern.2010.1080p.BluRay.x264-GRP",
				Trusted:   true,
				Downloads: 500,
				Format:    "srt",
				Text:      buildTimedSRT(15, 300),
			},
			{
				ID:        "valid-2",
				Kind:      "movie",
				Language:  "en",
				Title:     "The Lantern",
				Year:      2010,
				Release:   "The.Lantern.2010.1080p.BluRay.x264-OTHER",
				Trusted:   false,
				Downloads: 10,
				Format:    "srt",
				Text:      buildTimedSRT(15, 3300),
			},
		},
	}
	s, _ := svc(t, f, site)
	ctx := context.Background()
	wishAll(t, f, Want{Language: "en"})

	res, err := s.Search(ctx, f.movieFile, SearchOptions{}, domain.System)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Outcomes) != 1 || res.Outcomes[0].Result != "saved" {
		t.Fatalf("outcomes = %+v, want 1 saved", res.Outcomes)
	}

	rows, err := ListFor(ctx, f.st.Read(), f.movieFile)
	if err != nil {
		t.Fatal(err)
	}
	var saved *Subtitle
	for i := range rows {
		if rows[i].Place == "beside" && rows[i].State == "present" {
			saved = &rows[i]
			break
		}
	}
	if saved == nil || saved.SiteKey == nil || *saved.SiteKey != "valid-2" {
		t.Fatalf("saved subtitle = %+v, want site_key valid-2", saved)
	}
	if saved.IntegrityState == nil || *saved.IntegrityState != "ok" {
		t.Fatalf("saved integrity_state = %v, want ok", saved.IntegrityState)
	}

	failedEvents := eventsOf(t, f.st, EventFailed)
	if len(failedEvents) != 1 {
		t.Fatalf("expected 1 failed event for rejected truncated candidate, got %d", len(failedEvents))
	}

	// Unprobed file with only metadata runtime rejects broken timing but allows a structurally valid subtitle.
	t.Run("unprobed_metadata_runtime", func(t *testing.T) {
		uf := newFix(t)
		if err := uf.st.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE titles SET runtime_minutes = 120 WHERE id = ?`, uf.movie)
			return err
		}); err != nil {
			t.Fatal(err)
		}

		uSite := &fakesite.Site{
			Subs: []fakesite.Sub{
				{
					ID:        "broken-1",
					Kind:      "movie",
					Language:  "en",
					Title:     "The Lantern",
					Year:      2010,
					Release:   "The.Lantern.2010.1080p.BluRay.x264-GRP",
					Trusted:   true,
					Downloads: 500,
					Format:    "srt",
					Text:      "1\n00:00:10,000 --> 00:00:05,000\nBroken timing\n\n",
				},
				{
					ID:        "valid-short-2",
					Kind:      "movie",
					Language:  "en",
					Title:     "The Lantern",
					Year:      2010,
					Release:   "The.Lantern.2010.1080p.BluRay.x264-OTHER",
					Trusted:   false,
					Downloads: 10,
					Format:    "srt",
					Text:      buildTimedSRT(15, 300),
				},
			},
		}
		us, _ := svc(t, uf, uSite)
		wishAll(t, uf, Want{Language: "en"})

		ures, err := us.Search(ctx, uf.movieFile, SearchOptions{}, domain.System)
		if err != nil {
			t.Fatal(err)
		}
		if len(ures.Outcomes) != 1 || ures.Outcomes[0].Result != "saved" {
			t.Fatalf("outcomes = %+v, want 1 saved", ures.Outcomes)
		}

		urows, err := ListFor(ctx, uf.st.Read(), uf.movieFile)
		if err != nil {
			t.Fatal(err)
		}
		var usaved *Subtitle
		for i := range urows {
			if urows[i].Place == "beside" && urows[i].State == "present" {
				usaved = &urows[i]
				break
			}
		}
		if usaved == nil || usaved.SiteKey == nil || *usaved.SiteKey != "valid-short-2" {
			t.Fatalf("saved subtitle = %+v, want site_key valid-short-2", usaved)
		}
		if usaved.IntegrityState != nil {
			t.Fatalf("saved integrity_state = %v, want nil (unchecked without probed runtime)", *usaved.IntegrityState)
		}
		if ufailed := eventsOf(t, uf.st, EventFailed); len(ufailed) != 1 {
			t.Fatalf("expected 1 failed event for structurally corrupt candidate, got %d", len(ufailed))
		}
	})
}

func TestIntegrity_VerifyAllBatchesMultiFileRows(t *testing.T) {
	f := newFix(t)
	ctx := context.Background()
	setFileRuntimeSeconds(t, f.st, f.movieFile, 3600)
	setFileRuntimeSeconds(t, f.st, f.ep1File, 3600)

	movieRel := "The Lantern (2010)/The Lantern (2010) 1080p BluRay.mkv"
	ep1Rel := "Quiet Harbour (2019)/Season 01/Quiet Harbour - S01E02.mkv"

	// Create 60 beside subtitle rows across 2 files so VerifyAll exercises
	// both the FileID context cache and >1 batch flush (verifyBatchSize=50).
	var okIDs []int64
	var badIDs []int64
	for i := 0; i < 60; i++ {
		lang := fmt.Sprintf("l%02d", i)
		baseRel := movieRel
		fid := f.movieFile
		if i%2 == 1 {
			baseRel = ep1Rel
			fid = f.ep1File
		}
		content := buildTimedSRT(15, 3300)
		isBad := i%10 == 0
		if isBad {
			content = buildTimedSRT(15, 300)
		}
		subRel := writeBesideSub(t, f, baseRel, lang, content)
		id := insertBesideRow(t, f.st, fid, lang, subRel, "found", nil, nil)
		if isBad {
			badIDs = append(badIDs, id)
		} else {
			okIDs = append(okIDs, id)
		}
	}

	s := &Service{Store: f.st}
	if err := s.VerifyAll(ctx, &jobs.Run{}); err != nil {
		t.Fatal(err)
	}
	for _, id := range okIDs {
		row := getSubRow(t, f.st, id)
		if row.IntegrityState == nil || *row.IntegrityState != "ok" {
			t.Fatalf("row %d integrity_state = %v, want ok", id, row.IntegrityState)
		}
	}
	for _, id := range badIDs {
		row := getSubRow(t, f.st, id)
		if row.IntegrityState == nil || *row.IntegrityState != "bad" {
			t.Fatalf("row %d integrity_state = %v, want bad", id, row.IntegrityState)
		}
	}
}
