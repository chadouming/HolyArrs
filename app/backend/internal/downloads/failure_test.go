package downloads

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/mediafiles"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
)

func TestDownloaderFailureTurnsDownRemovesAndSearches(t *testing.T) {
	w, m := setup(t)
	clientSaysFailed(m)
	id := grabbed(t, w, m)
	if n, _ := m.SyncOnce(context.Background()); n != 0 {
		t.Fatalf("still active: %d", n)
	}
	wantFailedPath(t, w, id, "the Downloader says", false)
	if len(w.fake.deleted) != 1 || w.fake.deleted[0] != hash+"|false" {
		t.Fatalf("a client-caused failure must leave the files: deleted %v", w.fake.deleted)
	}
	_, _ = m.SyncOnce(context.Background())
	if len(w.failedSlots) != 1 {
		t.Fatal("failed twice")
	}
}

func TestFailedKeptInTheDownloaderWhenSettingOff(t *testing.T) {
	w, m := setup(t)
	w.setting(t, SettingRemoveFailed, "false")
	id := grabbed(t, w, m)
	clientSaysFailed(m)
	_, _ = m.SyncOnce(context.Background())
	wantFailedPath(t, w, id, "the Downloader says", false)
	if len(w.fake.deleted) != 0 {
		t.Fatalf("kept in the Downloader with the setting off: deleted %v", w.fake.deleted)
	}
}

func TestDangerousFilesFailTheDownload(t *testing.T) {
	w, m := setup(t)
	letChange(t, m)
	id := grabbed(t, w, m)
	w.fake.set(hash, map[string]any{"progress": 1.0, "state": "stalledUP"})
	w.fake.files[hash] = []map[string]any{{"name": "Dune/Dune.Part.Two.2024.1080p.WEB.H264-HOLYARRS.mkv.exe", "size": 100}, {"name": "Dune/readme.txt", "size": 1}}
	_, _ = m.SyncOnce(context.Background())
	wantFailedPath(t, w, id, "dangerous files: Dune.Part.Two.2024.1080p.WEB.H264-HOLYARRS.mkv.exe", true)
	if len(w.finshed) != 0 {
		t.Fatal("a dangerous Download was imported")
	}
}

func TestDangerousFilesAllowedWhenSettingOff(t *testing.T) {
	w, m := setup(t)
	w.setting(t, SettingFailDangerous, "false")
	id := grabbed(t, w, m)
	w.fake.set(hash, map[string]any{"progress": 1.0, "state": "stalledUP"})
	w.fake.files[hash] = []map[string]any{{"name": "setup.exe", "size": 100}}
	_, _ = m.SyncOnce(context.Background())
	if d := w.download(t, id); d.State != "finished" {
		t.Fatalf("want finished with the setting off, got %s", d.State)
	}
}

func TestMarkFailedByHand(t *testing.T) {
	w, m := setup(t)
	letChange(t, m)
	id := grabbed(t, w, m)
	if err := m.MarkFailed(context.Background(), id, domain.Actor{Kind: "user", ID: 1}, "luis"); err != nil {
		t.Fatal(err)
	}
	wantFailedPath(t, w, id, "marked failed by luis", true)
	if err := m.MarkFailed(context.Background(), id, domain.Actor{Kind: "user", ID: 1}, "luis"); !errors.Is(err, ErrNotActive) {
		t.Fatalf("second mark: %v", err)
	}
	var n int
	_ = w.s.ReadEvents().QueryRow("SELECT count(*) FROM events WHERE kind = 'download_failed' AND download_id = ?", id).Scan(&n)
	if n != 1 {
		t.Fatalf("%d failure events", n)
	}
}

func TestMarkFailedWhileStalled(t *testing.T) {
	w, m := setup(t)
	letChange(t, m)
	id := grabbed(t, w, m)
	w.clock = w.clock.Add(11 * time.Minute)
	_, _ = m.SyncOnce(context.Background())
	if w.slot(t).State != mediafiles.SlotStalled {
		t.Fatalf("slot %s", w.slot(t).State)
	}
	if err := m.MarkFailed(context.Background(), id, domain.System, ""); err != nil {
		t.Fatal(err)
	}
	wantFailedPath(t, w, id, "marked failed", true)
}

func TestStalledThenActedOn(t *testing.T) {
	w, m := setup(t)
	letChange(t, m)
	m.StallAfter, m.ActAfter = 0, 0
	id := grabbed(t, w, m)
	ctx := context.Background()
	w.clock = w.clock.Add(5*time.Hour + 59*time.Minute)
	_, _ = m.SyncOnce(ctx)
	if d := w.download(t, id); d.State != "downloading" {
		t.Fatalf("stalled too early: %s", d.State)
	}
	w.clock = w.clock.Add(time.Minute)
	_, _ = m.SyncOnce(ctx)
	if d := w.download(t, id); d.State != "stalled" || *d.StateReason != "no progress for 6 hours" {
		t.Fatalf("want stalled after 6 hours, got %s %v", d.State, d.StateReason)
	}
	m2 := &Manager{Store: w.s, Now: w.now, OnFailed: m.OnFailed, Sleep: m.Sleep}
	w.clock = w.clock.Add(17*time.Hour + 59*time.Minute)
	_, _ = m2.SyncOnce(ctx)
	if d := w.download(t, id); d.State != "stalled" {
		t.Fatalf("acted too early: %s", d.State)
	}
	w.clock = w.clock.Add(time.Minute)
	_, _ = m2.SyncOnce(ctx)
	wantFailedPath(t, w, id, "no progress for 24 hours", true)
}

func TestStallTimesComeFromSettings(t *testing.T) {
	w, m := setup(t)
	m.StallAfter, m.ActAfter = 0, 0
	w.setting(t, SettingStallAfter, "1")
	w.setting(t, SettingActAfter, "2")
	id := grabbed(t, w, m)
	w.clock = w.clock.Add(time.Hour)
	_, _ = m.SyncOnce(context.Background())
	if d := w.download(t, id); d.State != "stalled" {
		t.Fatalf("want stalled after 1 hour, got %s", d.State)
	}
	w.clock = w.clock.Add(time.Hour)
	_, _ = m.SyncOnce(context.Background())
	if d := w.download(t, id); d.State != "failed" {
		t.Fatalf("want failed after 2 hours, got %s", d.State)
	}
}

func TestQueuedPausedAndFullProgressNeverStall(t *testing.T) {
	w, m := setup(t)
	id := grabbed(t, w, m)
	ctx := context.Background()
	for _, c := range []struct {
		state    string
		progress float64
		want     string
	}{{"queuedDL", 0.2, "queued"}, {"pausedDL", 0.2, "paused"}, {"downloading", 1.0, "downloading"}} {
		w.fake.set(hash, map[string]any{"state": c.state, "progress": c.progress})
		w.clock = w.clock.Add(48 * time.Hour)
		_, _ = m.SyncOnce(ctx)
		if d := w.download(t, id); d.State != c.want {
			t.Fatalf("%s at %.1f: got %s, want %s", c.state, c.progress, d.State, c.want)
		}
	}
}

func TestTimeWaitingOrPausedNeverCounts(t *testing.T) {
	w, m := setup(t)
	id := grabbed(t, w, m)
	ctx := context.Background()
	w.fake.set(hash, map[string]any{"state": "pausedDL"})
	_, _ = m.SyncOnce(ctx)
	w.clock = w.clock.Add(48 * time.Hour)
	_, _ = m.SyncOnce(ctx)
	w.fake.set(hash, map[string]any{"state": "downloading"})
	_, _ = m.SyncOnce(ctx)
	if d := w.download(t, id); d.State != "downloading" {
		t.Fatalf("after a long pause: %s", d.State)
	}
	w.clock = w.clock.Add(11 * time.Minute)
	_, _ = m.SyncOnce(ctx)
	if d := w.download(t, id); d.State != "stalled" {
		t.Fatalf("no progress after resuming: %s", d.State)
	}
}

func TestProgressGoingBackDoesNotResetTheTimer(t *testing.T) {
	w, m := setup(t)
	id := grabbed(t, w, m)
	before := w.download(t, id).ProgressAt
	w.clock = w.clock.Add(5 * time.Minute)
	w.fake.set(hash, map[string]any{"progress": 0.1})
	_, _ = m.SyncOnce(context.Background())
	d := w.download(t, id)
	if d.Progress != 0.2 || !d.ProgressAt.Equal(*before) {
		t.Fatalf("progress %.2f at %v (was %v)", d.Progress, d.ProgressAt, before)
	}
}

func TestVanishedIsReplaced(t *testing.T) {
	w, m := setup(t)
	id := grabbed(t, w, m)
	w.fake.drop(hash)
	for range missedSyncs {
		w.advance(missWindow)
		_, _ = m.SyncOnce(context.Background())
	}
	d := w.download(t, id)
	if d.State != "vanished" || !strings.Contains(*d.StateReason, "removed outside HolyArrs") {
		t.Fatalf("download %s %v", d.State, d.StateReason)
	}
	if w.release(t).State != "turned_down" || w.slot(t).State != mediafiles.SlotFailed || len(w.failedSlots) != 1 || len(w.fake.deleted) != 0 {
		t.Fatalf("release %s slot %s searched %v deleted %v", w.release(t).State, w.slot(t).State, w.failedSlots, w.fake.deleted)
	}
}

func TestVanishedNeedsADecisionWhenSettingOff(t *testing.T) {
	w, m := setup(t)
	w.setting(t, SettingReplaceVanished, "false")
	id := grabbed(t, w, m)
	w.fake.drop(hash)
	for range missedSyncs {
		w.advance(missWindow)
		_, _ = m.SyncOnce(context.Background())
	}
	if d := w.download(t, id); d.State != "vanished" {
		t.Fatalf("download %s", d.State)
	}
	if w.release(t).State == "turned_down" || w.slot(t).State != mediafiles.SlotNeedsADecision || len(w.failedSlots) != 0 {
		t.Fatalf("release %s slot %s searched %v", w.release(t).State, w.slot(t).State, w.failedSlots)
	}
}

func TestReadFinishedFolderAndFile(t *testing.T) {
	dir := t.TempDir()
	job := filepath.Join(dir, "Job.Name")
	if err := os.MkdirAll(filepath.Join(job, "Sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(job, "movie.mkv"), make([]byte, 10), 0o644)
	_ = os.WriteFile(filepath.Join(job, "Sub", "movie.srt"), make([]byte, 3), 0o644)
	pm := []domain.PathMap{{Theirs: "/dl", Ours: dir}}
	save, files := readFinished(Job{ContentPath: "/dl/Job.Name"}, pm, "")
	if save != "/dl" || len(files) != 2 || files[0].Name != "Job.Name/Sub/movie.srt" && files[1].Name != "Job.Name/Sub/movie.srt" {
		t.Fatalf("folder: %s %+v", save, files)
	}
	save, files = readFinished(Job{ContentPath: "/dl/Job.Name/movie.mkv"}, pm, "")
	if save != "/dl/Job.Name" || len(files) != 1 || files[0].Name != "movie.mkv" || files[0].Size != 10 {
		t.Fatalf("file: %s %+v", save, files)
	}
	save, files = readFinished(Job{ContentPath: "/far/Away/x.mkv", Size: 7}, nil, "")
	if save != "/far/Away" || len(files) != 1 || files[0].Size != 7 {
		t.Fatalf("unreadable: %s %+v", save, files)
	}
}

func TestDiscardAfterRejectedImport(t *testing.T) {
	w, m := setup(t)
	letChange(t, m)
	id := grabbed(t, w, m)
	if err := m.Discard(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if len(w.fake.deleted) != 1 || w.fake.deleted[0] != hash+"|true" {
		t.Fatalf("deleted %v", w.fake.deleted)
	}
	w.setting(t, SettingRemoveFailed, "false")
	_ = m.Discard(context.Background(), id)
	if len(w.fake.deleted) != 1 {
		t.Fatal("removed with the setting off")
	}
}

func (w *world) release(t *testing.T) domain.Release {
	t.Helper()
	var r domain.Release
	if err := database.Get(context.Background(), w.s.Read(), "releases", w.relID, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func (w *world) setting(t *testing.T, key, value string) {
	t.Helper()
	if err := w.s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE settings SET value = ? WHERE grp = 'links' AND key = ?", value, key)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func grabbed(t *testing.T, w *world, m *Manager) int64 {
	t.Helper()
	id, err := m.Grab(context.Background(), w.slotID, w.relID, domain.System)
	if err != nil {
		t.Fatal(err)
	}
	w.fake.set(hash, map[string]any{"name": "Dune.Part.Two.2024.1080p.WEB.H264-HOLYARRS", "category": "holyarrs", "progress": 0.2, "state": "downloading"})
	if _, err := m.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	return id
}

func wantFailedPath(t *testing.T, w *world, id int64, reason string, removed bool) {
	t.Helper()
	d := w.download(t, id)
	if d.State != "failed" || d.StateReason == nil || !strings.Contains(*d.StateReason, reason) {
		t.Fatalf("download %s %v, want failed with %q", d.State, d.StateReason, reason)
	}
	if r := w.release(t); r.State != "turned_down" || !strings.Contains(r.Verdict["turned_down_reason"].(string), reason) {
		t.Fatalf("release %s %v", r.State, r.Verdict)
	}
	if sl := w.slot(t); sl.State != mediafiles.SlotFailed {
		t.Fatalf("slot %s", sl.State)
	}
	if len(w.failedSlots) != 1 || w.failedSlots[0] != w.slotID {
		t.Fatalf("searched again: %v", w.failedSlots)
	}
	gone := len(w.fake.deleted) == 1 && w.fake.deleted[0] == hash+"|true"
	if gone != removed {
		t.Fatalf("removed from the Downloader: %v (deleted %v)", gone, w.fake.deleted)
	}
}

func letChange(t *testing.T, m *Manager) {
	t.Helper()
	if err := m.Store.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE downloaders SET writable = 1")
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// With no save root and no path pair the owner set up, the disk is never read,
// even where the Downloader says the job is: only its own name and size are used.
func TestReadFinishedWithNoRootNeverReadsTheDisk(t *testing.T) {
	dir := t.TempDir()
	job := filepath.Join(dir, "complete", "Landman.S02E08.720p.BluRay.x264-GREASE")
	if err := os.MkdirAll(job, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(job, "Landman.S02E08.720p.BluRay.x264-GREASE.mkv"), make([]byte, 5), 0o644)
	if _, files := readFinished(Job{ContentPath: job, SavePath: filepath.Dir(job)}, nil, ""); len(files) != 0 {
		t.Fatalf("read the disk with no root set up: %+v", files)
	}
}

// A stalled download that is then paused or put back in the queue is no
// problem any more: the stall reason goes and its Slot is downloading again.
func TestPausedOrQueuedAfterStalledIsNoProblem(t *testing.T) {
	for _, c := range []struct{ client, want string }{{"pausedDL", "paused"}, {"queuedDL", "queued"}} {
		t.Run(c.want, func(t *testing.T) {
			w, m := setup(t)
			id := grabbed(t, w, m)
			ctx := context.Background()
			w.clock = w.clock.Add(11 * time.Minute)
			_, _ = m.SyncOnce(ctx)
			if d := w.download(t, id); d.State != "stalled" {
				t.Fatalf("want stalled first, got %s", d.State)
			}
			w.fake.set(hash, map[string]any{"state": c.client})
			_, _ = m.SyncOnce(ctx)
			d := w.download(t, id)
			if d.State != c.want || d.StateReason != nil {
				t.Fatalf("got %s with reason %v, want %s with none", d.State, d.StateReason, c.want)
			}
			if sl := w.slot(t); sl.State != mediafiles.SlotDownloading {
				t.Fatalf("slot %s, want downloading", sl.State)
			}
		})
	}
}

// A download that makes no progress keeps its row: the speed and the time
// left a Downloader gives for a job waiting in a long queue change on every
// sync, and writing each of them made every other read and write wait.
func TestNoProgressSpeedAndTimeLeftAreNotWritten(t *testing.T) {
	w, m := setup(t)
	id := grabbed(t, w, m)
	ctx := context.Background()
	w.fake.set(hash, map[string]any{"state": "downloading", "progress": 0.2, "dlspeed": 9000, "eta": 5000})
	_, _ = m.SyncOnce(ctx)
	before := w.download(t, id)
	if before.Speed != 9000 {
		t.Fatalf("speed %d, want the first reading written", before.Speed)
	}
	w.clock = w.clock.Add(time.Minute)
	w.fake.set(hash, map[string]any{"dlspeed": 9100, "eta": 4900})
	_, _ = m.SyncOnce(ctx)
	if d := w.download(t, id); !d.UpdatedAt.Equal(before.UpdatedAt) || d.Speed != 9000 {
		t.Fatalf("row written again for speed and time left alone: speed %d, updated %s (was %s)", d.Speed, d.UpdatedAt, before.UpdatedAt)
	}
	// Progress is news, and the speed and time left go with it.
	w.fake.set(hash, map[string]any{"progress": 0.3, "dlspeed": 9200, "eta": 4800})
	_, _ = m.SyncOnce(ctx)
	if d := w.download(t, id); d.Progress != 0.3 || d.Speed != 9200 {
		t.Fatalf("progress %v speed %d, want 0.3 and 9200", d.Progress, d.Speed)
	}
	// So is a speed that stops.
	w.fake.set(hash, map[string]any{"dlspeed": 0, "eta": 8640000})
	_, _ = m.SyncOnce(ctx)
	if d := w.download(t, id); d.Speed != 0 {
		t.Fatalf("speed %d, want 0 once the download stops moving", d.Speed)
	}
}

// A download taken as removed while its Downloader still has it comes back:
// it goes on, its Slot is downloading for it again and its Release is chosen.
func TestVanishedFoundAgainComesBack(t *testing.T) {
	for _, replace := range []string{"true", "false"} {
		t.Run("replace "+replace, func(t *testing.T) {
			w, m := setup(t)
			w.setting(t, SettingReplaceVanished, replace)
			id := grabbed(t, w, m)
			ctx := context.Background()
			w.fake.drop(hash)
			for range missedSyncs {
				w.advance(missWindow)
				_, _ = m.SyncOnce(ctx)
			}
			if d := w.download(t, id); d.State != "vanished" {
				t.Fatalf("download %s, want vanished first", d.State)
			}
			// Still gone: nothing comes back.
			m.vanishedBack(ctx)
			if d := w.download(t, id); d.State != "vanished" {
				t.Fatalf("download %s while the Downloader does not have it", d.State)
			}
			w.fake.set(hash, map[string]any{"name": "Dune.Part.Two.2024.1080p.WEB.H264-HOLYARRS", "category": "holyarrs", "state": "downloading", "progress": 0.4})
			m.vanishedBack(ctx)
			d := w.download(t, id)
			if d.State != "downloading" || d.StateReason != nil {
				t.Fatalf("download %s with reason %v, want downloading with none", d.State, d.StateReason)
			}
			sl := w.slot(t)
			if sl.State != mediafiles.SlotDownloading || sl.ActiveDownloadID == nil || *sl.ActiveDownloadID != id {
				t.Fatalf("slot %s for download %v, want downloading for %d", sl.State, sl.ActiveDownloadID, id)
			}
			if r := w.release(t); r.State != "chosen" {
				t.Fatalf("release %s, want chosen", r.State)
			}
			_, _ = m.SyncOnce(ctx)
			if d := w.download(t, id); d.State != "downloading" || d.Progress != 0.4 {
				t.Fatalf("after a sync: %s at %v", d.State, d.Progress)
			}
		})
	}
}

// A Slot that another download holds stays with it: the copy found again
// is left as it was.
func TestVanishedFoundAgainLeavesATakenSlot(t *testing.T) {
	w, m := setup(t)
	id := grabbed(t, w, m)
	ctx := context.Background()
	w.fake.drop(hash)
	for range missedSyncs {
		w.advance(missWindow)
		_, _ = m.SyncOnce(ctx)
	}
	other := w.oldRow(t, w.download(t, id).DownloaderID, "other", "downloading")
	w.exec(t, "UPDATE slots SET state = 'downloading', active_download_id = ? WHERE id = ?", other, w.slot(t).ID)
	w.fake.set(hash, map[string]any{"name": "Dune.Part.Two.2024.1080p.WEB.H264-HOLYARRS", "category": "holyarrs", "state": "downloading"})
	m.vanishedBack(ctx)
	if d := w.download(t, id); d.State != "vanished" {
		t.Fatalf("download %s, want it left vanished", d.State)
	}
	if sl := w.slot(t); sl.ActiveDownloadID == nil || *sl.ActiveDownloadID != other {
		t.Fatalf("slot moved to %v, want it left with %d", sl.ActiveDownloadID, other)
	}
}

// A Release someone turned down after its download vanished stays turned
// down: the copy the Downloader still has does not bring it back.
func TestVanishedFoundAgainLeavesATurnedDownRelease(t *testing.T) {
	for _, replace := range []string{"true", "false"} {
		t.Run("replace "+replace, func(t *testing.T) {
			w, m := setup(t)
			w.setting(t, SettingReplaceVanished, replace)
			id := grabbed(t, w, m)
			ctx := context.Background()
			w.fake.drop(hash)
			for range missedSyncs {
				w.advance(missWindow)
				_, _ = m.SyncOnce(ctx)
			}
			if d := w.download(t, id); d.State != "vanished" {
				t.Fatalf("download %s, want vanished first", d.State)
			}
			w.exec(t, `UPDATE releases SET state = 'turned_down', verdict = json_set(COALESCE(verdict, '{}'), '$.turned_down_reason', 'turned down by Ada on Needs a decision')
				WHERE id = ?`, w.release(t).ID)
			before := w.slot(t).State
			w.fake.set(hash, map[string]any{"name": "Dune.Part.Two.2024.1080p.WEB.H264-HOLYARRS", "category": "holyarrs", "state": "downloading", "progress": 0.4})
			m.vanishedBack(ctx)
			if d := w.download(t, id); d.State != "vanished" {
				t.Fatalf("download %s, want it left vanished", d.State)
			}
			if r := w.release(t); r.State != "turned_down" {
				t.Fatalf("release %s, want it left turned down", r.State)
			}
			if sl := w.slot(t); sl.State != before {
				t.Fatalf("slot %s, want it left %s", sl.State, before)
			}
		})
	}
}
