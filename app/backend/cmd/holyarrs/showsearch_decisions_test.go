package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/jobs"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
)

// A person who searches the whole show also means the episodes left on
// "needs a decision" by a failed download: each is searched again, the way
// searching that one episode by hand does. An episode whose download
// finished and waits for its import decision is left alone.
//
// route: POST /api/v1/titles/{id}/search
func TestWholeShowSearchRetriesFailedDownloads(t *testing.T) {
	t.Parallel()
	srv, s, _, _ := showSetup(t)
	u := srv.URL + "/api/v1"
	var add addedShow
	_ = json.Unmarshal(do(t, "POST", u+"/shows", `{"id":"tmdb:100"}`, 201), &add)
	tu := fmt.Sprintf("%s/titles/%d", u, add.Title.ID)
	ctx := context.Background()

	var eps struct {
		Episodes []EpisodeView `json:"episodes"`
	}
	_ = json.Unmarshal(do(t, "GET", tu+"/episodes?season=1", "", 200), &eps)
	if len(eps.Episodes) != 2 {
		t.Fatalf("season 1 %+v", eps.Episodes)
	}
	failed, waiting := eps.Episodes[0].Slots[0].ID, eps.Episodes[1].Slots[0].ID
	err := s.Write(ctx, func(tx *sql.Tx) error {
		now := database.FormatTime(time.Now())
		if _, err := tx.Exec(`INSERT INTO downloaders (id, created_at, created_by, updated_at, updated_by, name, adapter, protocol, url)
			VALUES (1, ?, '{}', ?, '{}', 'Lab', 'sabnzbd', 'usenet', 'http://x')`, now, now); err != nil {
			return err
		}
		for id, state := range map[int64]string{failed: "failed", waiting: "finished"} {
			if _, err := tx.Exec(`INSERT INTO downloads (id, created_at, created_by, updated_at, updated_by, downloader_id, external_id, state, started_by)
				VALUES (?, ?, '{}', ?, '{}', 1, ?, ?, '{}')`, id, now, now, fmt.Sprint("x", id), state); err != nil {
				return err
			}
			if _, err := tx.Exec("UPDATE slots SET state = 'needs_a_decision', active_download_id = ? WHERE id = ?", id, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var out struct {
		JobIDs []int64 `json:"job_ids"`
	}
	_ = json.Unmarshal(do(t, "POST", tu+"/search", "", 202), &out)
	var got []domain.Job
	for _, id := range out.JobIDs {
		var j domain.Job
		if err := database.Get(ctx, s.Read(), "jobs", id, &j); err != nil {
			t.Fatal(err)
		}
		got = append(got, j)
	}
	if len(got) != 1 || got[0].Kind != jobs.KindSearchSlot || got[0].Subject.ID != failed || got[0].Params["by_hand"] != true {
		t.Fatalf("whole show with a failed download and a waiting import: %+v", got)
	}
}
