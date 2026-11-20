package subtitles

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
)

// savedRow adds a subtitle HolyArrs saved beside a File.
func savedRow(t *testing.T, f *fix, fileID int64, language, rel, site string, made any, transcribed bool, at time.Time) {
	t.Helper()
	ts := database.FormatTime(at)
	err := f.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO subtitles (created_at, created_by, updated_at, updated_by, file_id, language, place, relative_path, site_name, origin, state, saved_at, made, transcribed)
			VALUES (?, ?, ?, ?, ?, ?, 'beside', ?, ?, 'fetched', 'present', ?, ?, ?)`, ts, sysActor, ts, sysActor, fileID, language, rel, site, ts, made, transcribed)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The page lists the subtitles HolyArrs saved, newest first, and says how
// each was made: from a site, by the translator, or from the audio. One found
// beside the video that HolyArrs did not save is not listed.
func TestDoneListsWhatHolyArrsSavedAndHow(t *testing.T) {
	f := newFix(t)
	at := now()
	savedRow(t, f, f.movieFile, "en", "m.en.srt", "Lab site", nil, false, at.Add(-3*time.Hour))
	savedRow(t, f, f.ep1File, "fr", "e1.fr.srt", "Built in", MadeTranslated, false, at.Add(-2*time.Hour))
	savedRow(t, f, f.ep2File, "en", "e2.en.srt", "Whisper", "ai_translated", true, at.Add(-1*time.Hour))
	savedRow(t, f, f.ep2File, "es", "e2.es.srt", "Lab site", "machine_translated", false, at.Add(-4*time.Hour))
	subRow(t, f, f.movieFile, "de", "beside", "present", "", "m.de.srt") // found, not saved by HolyArrs
	rows, counts, err := Done(context.Background(), f.st.Read(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("%d rows: %+v", len(rows), rows)
	}
	want := []struct{ how, lang, title string }{{HowSpeech, "en", "Quiet Harbour"}, {HowTranslated, "fr", "Quiet Harbour"}, {HowDownloaded, "en", "The Lantern"}, {HowDownloaded, "es", "Quiet Harbour"}}
	for i, w := range want {
		if rows[i].How != w.how || rows[i].Language != w.lang || rows[i].Title != w.title {
			t.Fatalf("row %d: %+v, want %+v", i, rows[i], w)
		}
	}
	if rows[0].Episode == nil || *rows[0].Episode != 3 || rows[0].By != "Whisper" || rows[0].SavedAt.IsZero() || rows[2].Kind != "movie" {
		t.Fatalf("details: %+v %+v", rows[0], rows[2])
	}
	if counts.Downloaded != 2 || counts.Translated != 1 || counts.Speech != 1 {
		t.Fatalf("counts %+v", counts)
	}
	// The list is cut to the limit; the counts are not.
	rows, counts, _ = Done(context.Background(), f.st.Read(), 2)
	if len(rows) != 2 || counts.Downloaded != 2 {
		t.Fatalf("limited: %d rows, %+v", len(rows), counts)
	}
	s := &Service{Store: f.st}
	o, err := s.Overview(context.Background())
	if err != nil || len(o.Done) != 4 || o.DoneCounts.Speech != 1 {
		t.Fatalf("overview: %+v %v", o.Done, err)
	}
}
