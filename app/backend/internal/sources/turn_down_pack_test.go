package sources

import (
	"context"
	"database/sql"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/jobs"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/mediafiles"
)

func TestTurnDownWholeAfterPartial_BlocksAllVersionSlotsAndLetsOtherPackWin(t *testing.T) {
	const packA = "Copper.Valley.S01.1080p.WEB-DL.H.264-GROUPA"
	const packB = "Copper.Valley.S01.1080p.WEB-DL.H.264-GROUPB"
	ts := &tvServer{answer: func(q url.Values) []string {
		if q.Get("ep") == "" {
			return []string{packA + "|4200", packB + "|3600"}
		}
		return nil
	}}
	f := seedShow(t, ts.start(t), nil)
	ctx := context.Background()

	// Seed Pack A having matched only S01E01 and S01E02 (leaving S01E03 outside matched_slot_ids)
	// and having an earlier partial turn-down for S01E01.
	var packAID int64
	if err := f.s.Write(ctx, func(tx *sql.Tx) error {
		rel := &domain.Release{
			SourceID:       f.srcID,
			Name:           packA,
			LinkRef:        "http://src/packa.torrent",
			Protocol:       "torrent",
			Size:           4200 << 20,
			Flags:          []string{},
			Parsed:         map[string]any{},
			MatchedSlotIDs: []int64{f.slots["S01E01"], f.slots["S01E02"]},
			Verdict:        map[string]any{},
			FoundBy:        "auto_search",
			State:          "chosen",
		}
		id, err := database.Insert(tx, "releases", rel, domain.System)
		if err != nil {
			return err
		}
		packAID = id
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	sr := &Searcher{Store: f.s}
	if err := sr.TurnDownFor(ctx, packAID, f.slots["S01E01"], "bad ep 1", domain.System); err != nil {
		t.Fatal(err)
	}
	var gotRel domain.Release
	if err := database.Get(ctx, f.s.Read(), "releases", packAID, &gotRel); err != nil {
		t.Fatal(err)
	}
	if len(TurnedDownSlots(gotRel)) != 1 || TurnedDownSlots(gotRel)[0] != f.slots["S01E01"] {
		t.Fatalf("expected partial turned_down_slot_ids [S01E01], got %+v", gotRel.Verdict)
	}

	// Whole-release TurnDown must clear turned_down_slot_ids and block every Slot of that Version.
	if err := sr.TurnDown(ctx, packAID, "bad pack whole", domain.System); err != nil {
		t.Fatal(err)
	}
	if err := database.Get(ctx, f.s.Read(), "releases", packAID, &gotRel); err != nil {
		t.Fatal(err)
	}
	if got := TurnedDownSlots(gotRel); len(got) != 0 {
		t.Fatalf("expected turned_down_slot_ids cleared, got %v", got)
	}
	for _, label := range []string{"S01E01", "S01E02", "S01E03"} {
		td, err := sr.turnedDown(ctx, f.slots[label])
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(td, packA) {
			t.Fatalf("expected %s turnedDown list to contain %s, got %v", label, packA, td)
		}
	}

	// HandleSeason must refuse Pack A and choose Pack B for all 3 episodes.
	j := runKind(t, f.fixture, sr, jobs.KindSearchSeason, &domain.Subject{Type: "part", ID: f.parts[1]}, map[string]any{"version_id": f.verID, "by_hand": true})
	if j.State != "done" {
		t.Fatalf("job %s %v", j.State, j.Error)
	}
	chosenID, ok := j.Result["chosen_release_id"].(float64)
	if !ok || chosenID == 0 {
		t.Fatalf("expected chosen_release_id in result, got %+v", j.Result)
	}
	var winner domain.Release
	if err := database.Get(ctx, f.s.Read(), "releases", int64(chosenID), &winner); err != nil {
		t.Fatal(err)
	}
	if winner.Name != packB {
		t.Fatalf("expected %s to win, got %s", packB, winner.Name)
	}
	for _, label := range []string{"S01E01", "S01E02", "S01E03"} {
		if st := f.state(t, label); st != mediafiles.SlotChosen {
			t.Fatalf("%s state = %s, want chosen", label, st)
		}
	}
}

func TestTurnedDown_MagnetBtihExternalIDAndCaseInsensitiveMatch(t *testing.T) {
	const hashMagnet = "1111111111111111111111111111111111111111"
	const hashExt = "2222222222222222222222222222222222222222"
	const oldByName = "Copper.Valley.S01.1080p.WEB-DL.H.264-OLDBYNAME"
	const cleanPack = "Copper.Valley.S01.1080p.WEB-DL.H.264-CLEAN"

	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("t") == "caps" {
			_, _ = w.Write([]byte(tvCaps))
			return
		}
		if q.Get("ep") != "" {
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel></channel></rss>`))
			return
		}
		pub := time.Now().UTC().Add(-time.Hour).Format(time.RFC1123Z)
		body := `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel>` +
			`<item><title>Copper.Valley.S01.1080p.WEB-DL.H.264-RENAMEDMAGNET</title><pubDate>` + pub + `</pubDate>` +
			`<enclosure url="magnet:?xt=urn:btih:` + strings.ToUpper(hashMagnet) + `&amp;dn=renamed" length="4194304000" type="application/x-bittorrent"/>` +
			`<torznab:attr name="seeders" value="80"/></item>` +
			`<item><title>Copper.Valley.S01.1080p.WEB-DL.H.264-RENAMEDEXT</title><pubDate>` + pub + `</pubDate>` +
			`<enclosure url="http://src/renamedext.torrent" length="4194304000" type="application/x-bittorrent"/>` +
			`<torznab:attr name="seeders" value="70"/><torznab:attr name="infohash" value="` + strings.ToUpper(hashExt) + `"/></item>` +
			`<item><title>` + strings.ToLower(oldByName) + `</title><pubDate>` + pub + `</pubDate>` +
			`<enclosure url="http://src/lower.torrent" length="4194304000" type="application/x-bittorrent"/>` +
			`<torznab:attr name="seeders" value="60"/></item>` +
			`<item><title>` + cleanPack + `</title><pubDate>` + pub + `</pubDate>` +
			`<enclosure url="http://src/clean.torrent" length="3774873600" type="application/x-bittorrent"/>` +
			`<torznab:attr name="seeders" value="50"/></item>` +
			`</channel></rss>`
		_, _ = w.Write([]byte(body))
	})

	f := seedShow(t, srv.URL+"/api", nil)
	ctx := context.Background()

	if err := f.s.Write(ctx, func(tx *sql.Tx) error {
		// 1) Turned-down pack with NULL info_hash and magnet btih in link_ref.
		if _, err := database.Insert(tx, "releases", &domain.Release{
			SourceID: f.srcID, Name: "Copper.Valley.S01.1080p.WEB-DL.H.264-ORIGMAGNET",
			LinkRef: "magnet:?xt=urn:btih:" + hashMagnet + "&dn=orig", Protocol: "torrent", Size: 4000 << 20,
			Flags: []string{}, Parsed: map[string]any{}, MatchedSlotIDs: []int64{f.slots["S01E01"]},
			Verdict: map[string]any{"turned_down_reason": "bad"}, FoundBy: "auto_search", State: "turned_down",
		}, domain.System); err != nil {
			return err
		}
		// 2) Turned-down pack with NULL info_hash and http link, hash only in downloads.external_id.
		relExt := &domain.Release{
			SourceID: f.srcID, Name: oldByName,
			LinkRef: "http://src/oldext.torrent", Protocol: "torrent", Size: 4000 << 20,
			Flags: []string{}, Parsed: map[string]any{}, MatchedSlotIDs: []int64{f.slots["S01E01"]},
			Verdict: map[string]any{"turned_down_reason": "bad"}, FoundBy: "auto_search", State: "turned_down",
		}
		relExtID, err := database.Insert(tx, "releases", relExt, domain.System)
		if err != nil {
			return err
		}
		dr := &domain.Downloader{
			Writable: true, Name: "Q", Adapter: "qbittorrent", Protocol: "torrent", URL: "http://127.0.0.1:1",
			Category: "tv", Priority: 1, PathMap: []domain.PathMap{}, AfterImport: "leave", State: "ok", AllowLocal: true,
		}
		if _, err := database.Insert(tx, "downloaders", dr, domain.System); err != nil {
			return err
		}
		if _, err := database.Insert(tx, "downloads", &domain.Download{
			DownloaderID: dr.ID, ReleaseID: &relExtID, SlotIDs: []int64{f.slots["S01E01"]},
			ExternalID: strings.ToUpper(hashExt), State: "failed", FilesSeen: []domain.SeenFile{}, StartedBy: domain.System,
		}, domain.System); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	sr := &Searcher{Store: f.s}
	j := runKind(t, f.fixture, sr, jobs.KindSearchSeason, &domain.Subject{Type: "part", ID: f.parts[1]}, map[string]any{"version_id": f.verID, "by_hand": true})
	if j.State != "done" {
		t.Fatalf("job %s %v", j.State, j.Error)
	}
	chosenID, ok := j.Result["chosen_release_id"].(float64)
	if !ok || chosenID == 0 {
		t.Fatalf("expected chosen_release_id, got %+v", j.Result)
	}
	var winner domain.Release
	if err := database.Get(ctx, f.s.Read(), "releases", int64(chosenID), &winner); err != nil {
		t.Fatal(err)
	}
	if winner.Name != cleanPack {
		t.Fatalf("expected %s to win after refusing turned-down packs, got %s", cleanPack, winner.Name)
	}
}

func TestHandleSeason_ByHandPicksNeedsADecisionAndRestores(t *testing.T) {
	ts := &tvServer{answer: func(q url.Values) []string {
		return nil
	}}
	f := seedShow(t, ts.start(t), func(label string, sl *mediafiles.Slot) {
		switch label {
		case "S01E01":
			sl.State = mediafiles.SlotNeedsADecision
		case "S01E03":
			sl.State = mediafiles.SlotBetterWanted
		}
	})
	sr := &Searcher{Store: f.s}
	q := jobs.New(f.s, jobs.Options{Workers: 1, BaseBackoff: time.Hour, PollInterval: 5 * time.Millisecond})
	sr.Queue = q
	q.Handle(jobs.KindSearchSeason, sr.HandleSeason)
	ctx := context.Background()
	if err := q.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer q.Stop()

	id, _, err := q.Enqueue(ctx, jobs.KindSearchSeason, &domain.Subject{Type: "part", ID: f.parts[1]}, map[string]any{"version_id": f.verID, "by_hand": true}, "", domain.System)
	if err != nil {
		t.Fatal(err)
	}
	var j domain.Job
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := database.Get(ctx, f.s.Read(), "jobs", id, &j); err != nil {
			t.Fatal(err)
		}
		if j.State == "done" || j.State == "failed" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if j.State != "done" {
		t.Fatalf("job %s %v", j.State, j.Error)
	}
	if n, _ := j.Result["episodes_queued"].(float64); n != 3 {
		t.Fatalf("expected all 3 episodes (including needs_a_decision) queued, got %+v", j.Result)
	}
	if st := f.state(t, "S01E01"); st != mediafiles.SlotNeedsADecision {
		t.Fatalf("S01E01 state = %s, want needs_a_decision", st)
	}
	if st := f.state(t, "S01E02"); st != mediafiles.SlotWanted {
		t.Fatalf("S01E02 state = %s, want wanted", st)
	}
	if st := f.state(t, "S01E03"); st != mediafiles.SlotBetterWanted {
		t.Fatalf("S01E03 state = %s, want better_wanted", st)
	}
}
