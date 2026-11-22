package sources

import (
	"bytes"
	"context"
	"database/sql"
	"log"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/jobs"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/mediafiles"
)

// A Release named after one part of a show, with a running number, is that
// part's episode: "Copper Valley Ridge Line - 01" is season 2 episode 1, not
// the show's first episode.
func TestSearch_PartNameCountsInsideThePart(t *testing.T) {
	const rel = "[Group] Copper Valley Ridge Line - 01 [1080p WEB-DL x264]"
	ts := &tvServer{answer: func(q url.Values) []string { return []string{rel} }}
	f := seedShow(t, ts.start(t), nil)
	err := f.s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE titles SET part_names = ? WHERE id = ?`,
			`[{"names":["Copper Valley Ridge Line"],"season":2,"offset":0,"source":"season_name"}]`, f.titleID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	sr := &Searcher{Store: f.s}
	// Asked for the show's first episode, the Release is not it.
	j := runKind(t, f.fixture, sr, jobs.KindSearchSlot, &domain.Subject{Type: "slot", ID: f.slots["S01E01"]}, nil)
	if j.State != "done" {
		t.Fatalf("job %s %+v", j.State, j.Error)
	}
	if st := f.state(t, "S01E01"); st != mediafiles.SlotWanted {
		t.Fatalf("S01E01 is %s: the part's episode 1 was taken for the show's", st)
	}
	if r := relByName(t, f.fixture, rel); !hasCode(r, "match.not_asked") {
		t.Fatalf("for S01E01: %s %v", r.State, reasonCodes(r))
	}
	// Asked for season 2 episode 1, it is chosen.
	j = runKind(t, f.fixture, sr, jobs.KindSearchSlot, &domain.Subject{Type: "slot", ID: f.slots["S02E01"]}, nil)
	if j.State != "done" {
		t.Fatalf("job %s %+v", j.State, j.Error)
	}
	if st := f.state(t, "S02E01"); st != mediafiles.SlotChosen {
		r := relByName(t, f.fixture, rel)
		t.Fatalf("S02E01 is %s: %s %v", st, r.State, reasonCodes(r))
	}
}

// The part names of a Slot's show are read for a pack's files; a show in
// another episode order, or a Slot that is not there, has none.
func TestPartsOfSlot(t *testing.T) {
	f := seedShow(t, (&tvServer{answer: func(url.Values) []string { return nil }}).start(t), nil)
	ctx := context.Background()
	set := func(q string, args ...any) {
		t.Helper()
		if err := f.s.Write(ctx, func(tx *sql.Tx) error { _, err := tx.Exec(q, args...); return err }); err != nil {
			t.Fatal(err)
		}
	}
	set(`UPDATE titles SET part_names = ? WHERE id = ?`, `[{"names":["Copper Valley Ridge Line"],"season":2,"offset":0,"source":"season_name"}]`, f.titleID)
	got := mediafiles.PartsOfSlot(ctx, f.s.Read(), f.slots["S02E01"])
	if len(got) != 1 || got[0].Season != 2 || got[0].Names[0] != "Copper Valley Ridge Line" {
		t.Fatalf("got %+v", got)
	}
	if got := mediafiles.PartsOfSlot(ctx, f.s.Read(), 999999); got != nil {
		t.Fatalf("an unknown Slot gave %+v", got)
	}
	set(`UPDATE titles SET episode_order = 'absolute' WHERE id = ?`, f.titleID)
	if got := mediafiles.PartsOfSlot(ctx, f.s.Read(), f.slots["S02E01"]); got != nil {
		t.Fatalf("absolute order gave %+v", got)
	}
}

// A feed item named after a part of a show is matched like a search result.
func TestFeed_PartNameCountsInsideThePart(t *testing.T) {
	const rel = "[Group] Copper Valley Ridge Line - 01 [1080p WEB-DL x264]"
	ts := &tvServer{answer: func(url.Values) []string { return []string{rel} }}
	f := seedShow(t, ts.start(t), nil)
	if err := f.s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE titles SET part_names = ? WHERE id = ?`,
			`[{"names":["Copper Valley Ridge Line"],"season":2,"offset":0,"source":"season_name"}]`, f.titleID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	j := runKind(t, f.fixture, &Searcher{Store: f.s}, jobs.KindFeedCheck, nil, nil)
	if j.State != "done" {
		t.Fatalf("feed %s %+v", j.State, j.Error)
	}
	if st := f.state(t, "S02E01"); st != mediafiles.SlotChosen {
		t.Fatalf("S02E01 is %s", st)
	}
	if st := f.state(t, "S01E01"); st != mediafiles.SlotWanted {
		t.Fatalf("S01E01 is %s", st)
	}
}

// A failed read is said in the log: with no part names a pack's files map by
// running number, and that should not happen without a trace. A Slot that is
// simply not there says nothing.
func TestPartsOfSlotLogsAFailedRead(t *testing.T) {
	f := seedShow(t, (&tvServer{answer: func(url.Values) []string { return nil }}).start(t), nil)
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	if got := mediafiles.PartsOfSlot(context.Background(), f.s.Read(), 999999); got != nil || buf.Len() != 0 {
		t.Fatalf("a missing Slot: %+v, log %q", got, buf.String())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := mediafiles.PartsOfSlot(ctx, f.s.Read(), f.slots["S02E01"]); got != nil {
		t.Fatalf("got %+v", got)
	}
	if !strings.Contains(buf.String(), "part names") {
		t.Fatalf("log %q", buf.String())
	}
}
