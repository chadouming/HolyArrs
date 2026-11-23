package downloads

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/jobs"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/mediafiles"
)

func TestDecidePackTurnDown_UntiesSlotsClearsPackFilesAndQueuesSeasonSearch(t *testing.T) {
	t.Parallel()
	x, ids := seedPack(t)
	ctx := context.Background()

	var partID, verID int64
	if err := x.s.Write(ctx, func(tx *sql.Tx) error {
		var titleID, item1, lfID int64
		if err := tx.QueryRow(`SELECT i.title_id, i.id, s.version_id, v.library_folder_id
			FROM slots s JOIN items i ON i.id = s.item_id JOIN versions v ON v.id = s.version_id WHERE s.id = ?`, ids[0]).
			Scan(&titleID, &item1, &verID, &lfID); err != nil {
			return err
		}
		p := &mediafiles.Part{
			TitleID: titleID, Kind: "season", Number: ptr(1), Name: "Season 1",
			KeepUpToDate: true, SourceIDs: map[string]string{}, Pressings: []mediafiles.Pressing{},
		}
		if _, err := database.Insert(tx, "parts", p, domain.System); err != nil {
			return err
		}
		partID = p.ID
		if _, err := tx.Exec("UPDATE items SET part_id = ? WHERE title_id = ?", partID, titleID); err != nil {
			return err
		}
		// Attach a present file to ids[2] so it has a copy and should move to better_wanted.
		f := &mediafiles.File{
			SlotIDs: []int64{ids[2]}, VersionID: verID, RelativePath: "Copper Valley/S01E03.mkv",
			Size: 1000, AddedAt: time.Now().UTC(), Languages: []string{}, MediaInfo: map[string]any{},
			Origin: "imported", PlacedBy: "hardlink", Role: "main", State: "present",
		}
		if _, err := database.Insert(tx, "files", f, domain.System); err != nil {
			return err
		}
		if _, err := tx.Exec("UPDATE slots SET file_id = ? WHERE id = ?", f.ID, ids[2]); err != nil {
			return err
		}
		if _, err := tx.Exec("UPDATE slots SET active_download_id = ? WHERE id IN (?, ?)", x.dlID, ids[0], ids[2]); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE downloads SET pack_files = '[{"index":0,"items":[1],"slots":[]}]' WHERE id = ?`, x.dlID); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE releases SET verdict = json_set(COALESCE(verdict, '{}'), '$.turned_down_slot_ids', json(?)) WHERE id = ?`,
			"["+itoa(ids[0])+"]", x.relID); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	b := do(t, "POST", x.srv.URL+"/api/v1/needs-decision/"+itoa(ids[0])+"/decide", `{"action":"turn_down","pack":true}`, 202)
	var res struct {
		Action string  `json:"action"`
		JobID  int64   `json:"job_id"`
		JobIDs []int64 `json:"job_ids"`
	}
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatal(err)
	}
	if res.Action != "turn_down" || len(res.JobIDs) != 1 || res.JobID == 0 {
		t.Fatalf("unexpected response %s", b)
	}

	// 1. Release is turned down whole with turned_down_slot_ids cleared.
	var rstate string
	var tdSlots sql.NullString
	if err := x.s.Read().QueryRow("SELECT state, json_extract(verdict, '$.turned_down_slot_ids') FROM releases WHERE id = ?", x.relID).
		Scan(&rstate, &tdSlots); err != nil {
		t.Fatal(err)
	}
	if rstate != "turned_down" || tdSlots.Valid {
		t.Fatalf("release state=%s turned_down_slot_ids=%v", rstate, tdSlots)
	}

	// 2. Slot without copy -> wanted, slot with copy -> better_wanted, active_download_id cleared on both.
	for _, tc := range []struct {
		id   int64
		want string
	}{
		{ids[0], string(mediafiles.SlotWanted)},
		{ids[2], string(mediafiles.SlotBetterWanted)},
	} {
		var st string
		var active sql.NullInt64
		if err := x.s.Read().QueryRow("SELECT state, active_download_id FROM slots WHERE id = ?", tc.id).Scan(&st, &active); err != nil {
			t.Fatal(err)
		}
		if st != tc.want || active.Valid {
			t.Fatalf("slot %d state=%s (want %s) active_download_id=%v", tc.id, st, tc.want, active)
		}
	}

	// 3. downloads.pack_files is cleared.
	var packFiles sql.NullString
	if err := x.s.Read().QueryRow("SELECT pack_files FROM downloads WHERE id = ?", x.dlID).Scan(&packFiles); err != nil {
		t.Fatal(err)
	}
	if packFiles.Valid {
		t.Fatalf("expected pack_files NULL, got %q", packFiles.String)
	}

	// 4. GET /api/v1/needs-decision is empty and downloads/view moved out of needs_a_decision.
	var nd struct {
		Count int            `json:"count"`
		Items []DecisionView `json:"items"`
	}
	_ = json.Unmarshal(do(t, "GET", x.srv.URL+"/api/v1/needs-decision", "", 200), &nd)
	if nd.Count != 0 || len(nd.Items) != 0 {
		t.Fatalf("expected empty needs-decision, got %+v", nd)
	}
	var view struct {
		Downloads []DownloadRow `json:"downloads"`
	}
	_ = json.Unmarshal(do(t, "GET", x.srv.URL+"/api/v1/downloads/view", "", 200), &view)
	if len(view.Downloads) != 1 || view.Downloads[0].Phase == "needs_a_decision" {
		t.Fatalf("expected download row out of needs_a_decision, got %+v", view.Downloads)
	}

	// 5. Enqueued job is search_season with by_hand: true and version_id.
	var job domain.Job
	if err := database.Get(ctx, x.s.Read(), "jobs", res.JobID, &job); err != nil {
		t.Fatal(err)
	}
	if job.Kind != jobs.KindSearchSeason || job.Subject == nil || job.Subject.Type != "part" || job.Subject.ID != partID {
		t.Fatalf("expected search_season for part %d, got %+v", partID, job)
	}
	if byHand, _ := job.Params["by_hand"].(bool); !byHand {
		t.Fatalf("expected by_hand=true in job params %+v", job.Params)
	}
	if vid, _ := job.Params["version_id"].(float64); int64(vid) != verID {
		t.Fatalf("expected version_id=%d in job params %+v", verID, job.Params)
	}
	if job.DedupeKey == nil || !strings.HasPrefix(*job.DedupeKey, "hand_search_season:") {
		t.Fatalf("unexpected job dedupe_key %v", job.DedupeKey)
	}
}
