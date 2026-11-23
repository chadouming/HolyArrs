package downloads

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/mediafiles"
)

// vanishedReasonPrefix starts the reason of a download its Downloader stopped listing.
const vanishedReasonPrefix = "removed outside HolyArrs:"

// backMax is how many downloads one pass brings back, so a long list never
// holds the other writes up; the next pass takes the rest.
const backMax = 200

func isVanishedTurnDown(why, stateReason string) bool {
	if stateReason == "" {
		return false
	}
	return why == turnedDownWhy("vanished", stateReason) || why == "the download failed: "+stateReason
}

// vanishedBack brings back the downloads that were taken as removed from
// their Downloader while it still has them: a list read short, or a job
// removed and added again. Such a download goes on where it was and takes
// back each of its Slots that nothing else holds. One whose Slots all moved
// on (another download, a File in the Library) stays as it is.
func (m *Manager) vanishedBack(ctx context.Context) {
	if m.Store == nil {
		return
	}
	db := m.Store.Read()
	gone, err := database.Select[domain.Download](ctx, db, "downloads", "state = 'vanished' AND state_reason LIKE ? ORDER BY id", vanishedReasonPrefix+"%")
	if err != nil || len(gone) == 0 {
		return
	}
	byDownloader := map[int64][]domain.Download{}
	for _, d := range gone {
		byDownloader[d.DownloaderID] = append(byDownloader[d.DownloaderID], d)
	}
	left := backMax
	for did, list := range byDownloader {
		var dr domain.Downloader
		if database.Get(ctx, db, "downloaders", did, &dr) != nil || dr.Removed {
			continue
		}
		jobs, ok := m.jobsNow(ctx, dr)
		if !ok {
			continue
		}
		pos := map[string]int{}
		for i, j := range jobs {
			if j.State != StateFailed {
				pos[j.ID] = i
			}
		}
		list = slices.DeleteFunc(list, func(d domain.Download) bool { _, ok := pos[d.ExternalID]; return !ok })
		// In the Downloader's order: of two copies for one Slot, the one that
		// downloads first takes it.
		slices.SortStableFunc(list, func(a, b domain.Download) int { return pos[a.ExternalID] - pos[b.ExternalID] })
		for _, d := range list {
			if left == 0 || ctx.Err() != nil {
				return
			}
			if m.bringBack(ctx, d) {
				left--
			}
		}
	}
}

// jobsNow is what a Downloader holds now, from its fresh snapshot or read
// from it directly; false when neither can be had.
func (m *Manager) jobsNow(ctx context.Context, dr domain.Downloader) ([]Job, bool) {
	if m.storeOn() {
		if v := m.Sync.View(dr.ID); v.Fresh {
			return snapshotJobs(v, dr.Categories()), true
		}
	}
	e, err := m.engine(ctx, dr)
	if err != nil {
		return nil, false
	}
	jobs, err := listCategories(ctx, e, dr.Categories())
	return jobs, err == nil
}

// bringBack puts one vanished download back to downloading with the Slots it
// can take back, and its Release back to chosen. It reports whether it did.
func (m *Manager) bringBack(ctx context.Context, d domain.Download) bool {
	var took []int64
	now := m.now()
	vanishWhy := ""
	if d.StateReason != nil {
		vanishWhy = *d.StateReason
	}
	err := m.Store.Write(ctx, func(tx *sql.Tx) error {
		took = took[:0]
		var cur string
		if err := tx.QueryRow("SELECT state FROM downloads WHERE id = ?", d.ID).Scan(&cur); err != nil || cur != "vanished" {
			return err
		}
		// A Release someone turned down stays turned down: only the turn-down
		// that the vanishing itself made is taken back.
		var notFor []int64
		if d.ReleaseID != nil {
			var rstate string
			var why, only sql.NullString
			if err := tx.QueryRow(`SELECT state, json_extract(verdict, '$.turned_down_reason'), json_extract(verdict, '$.turned_down_slot_ids')
				FROM releases WHERE id = ?`, *d.ReleaseID).Scan(&rstate, &why, &only); err == nil {
				if only.Valid {
					_ = json.Unmarshal([]byte(only.String), &notFor)
				}
				if rstate == "turned_down" && !isVanishedTurnDown(why.String, vanishWhy) && len(notFor) == 0 {
					return nil
				}
			}
		}
		for _, sid := range d.SlotIDs {
			if slices.Contains(notFor, sid) {
				continue
			}
			var state string
			var active sql.NullInt64
			if err := tx.QueryRow("SELECT state, active_download_id FROM slots WHERE id = ?", sid).Scan(&state, &active); err != nil {
				continue
			}
			var path []mediafiles.SlotState
			switch mediafiles.SlotState(state) {
			case mediafiles.SlotNeedsADecision:
				path = []mediafiles.SlotState{mediafiles.SlotChosen, mediafiles.SlotDownloading}
			case mediafiles.SlotFailed, mediafiles.SlotWanted, mediafiles.SlotBetterWanted:
				path = []mediafiles.SlotState{mediafiles.SlotSearching, mediafiles.SlotChosen, mediafiles.SlotDownloading}
			default:
				continue // downloading, importing, in the Library: it moved on
			}
			if active.Valid && active.Int64 != d.ID {
				// Another download that still runs, or waits for its import, keeps the Slot.
				var other string
				if err := tx.QueryRow("SELECT state FROM downloads WHERE id = ?", active.Int64).Scan(&other); err == nil &&
					(isActive(other) || other == "finished" || other == "seeding") {
					continue
				}
			}
			for _, to := range path {
				if err := mediafiles.MoveSlotTx(tx, sid, to, "", domain.System); err != nil {
					return err
				}
			}
			if _, err := tx.Exec("UPDATE slots SET active_download_id = ?, chosen_release_id = COALESCE(?, chosen_release_id) WHERE id = ?", d.ID, d.ReleaseID, sid); err != nil {
				return err
			}
			took = append(took, sid)
		}
		if len(took) == 0 {
			return nil
		}
		if d.ReleaseID != nil {
			if _, err := tx.Exec(`UPDATE releases SET state = 'chosen', verdict = json_remove(COALESCE(verdict, '{}'), '$.turned_down_reason'), updated_at = ?
				WHERE id = ? AND state = 'turned_down' AND json_extract(verdict, '$.turned_down_reason') IN (?, ?)`,
				database.FormatTime(now), *d.ReleaseID, turnedDownWhy("vanished", vanishWhy), "the download failed: "+vanishWhy); err != nil {
				return err
			}
		}
		// The stall timer starts again: the time it was taken as removed is not
		// time without progress.
		d.State, d.StateReason, d.ProgressAt, d.CleanedAt = "downloading", nil, &now, nil
		return database.Update(tx, "downloads", &d, domain.System)
	})
	if err != nil || len(took) == 0 {
		return false
	}
	m.seen(d.ID)
	m.publish("download", d)
	for _, sid := range took {
		m.publish("slot", map[string]any{"id": sid, "state": mediafiles.SlotDownloading, "reason": ""})
	}
	m.event(ctx, &d, "download_changed", "Download found in the Downloader again: it goes on")
	return true
}
