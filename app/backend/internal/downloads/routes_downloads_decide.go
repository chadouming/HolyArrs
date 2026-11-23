package downloads

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/mediafiles"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/jobs"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/webserver"

	"github.com/HolyArrs/holyarrs/app/backend/internal/importer"
	"github.com/HolyArrs/holyarrs/app/backend/internal/sources"
)

func (a *Web) removeDownload(w http.ResponseWriter, r *http.Request) {
	id, ok := idIn(w, r, "download")
	if !ok {
		return
	}
	st, err := a.readDownloaderSettings(r.Context())
	if err != nil {
		webserver.Internal(w, "api", err)
		return
	}
	var in struct {
		DeleteFiles *bool `json:"delete_files"`
		TurnDown    *bool `json:"turn_down"`
		SearchAgain *bool `json:"search_again"`
	}
	if r.ContentLength != 0 && !webserver.Decode(w, r, &in, `body is {"delete_files": bool, "turn_down": bool, "search_again": bool}, each optional (defaults in Settings > Downloaders)`) {
		return
	}
	c := RemoveChoice{DeleteFiles: st.RemoveDeleteFiles, TurnDown: st.RemoveTurnDown, SearchAgain: st.RemoveSearchAgain}
	if in.DeleteFiles != nil {
		c.DeleteFiles = *in.DeleteFiles
	}
	if in.TurnDown != nil {
		c.TurnDown = *in.TurnDown
	}
	if in.SearchAgain != nil {
		c.SearchAgain = *in.SearchAgain
	}
	who := ""
	if p, ok := webserver.From(r.Context()); ok {
		who = p.Name
	}
	note, err := a.Manager().RemoveSaid(r.Context(), id, c, actor(r), who)
	switch {
	case errors.Is(err, database.ErrNotFound), errors.Is(err, sql.ErrNoRows):
		webserver.Fail(w, http.StatusNotFound, "no such download")
	case errors.Is(err, ErrNotActive), errors.Is(err, ErrImporting), errors.Is(err, ErrDownloaderReadOnly):
		webserver.Fail(w, http.StatusConflict, err.Error())
	case err != nil:
		webserver.Internal(w, "api", err)
	default:
		var d domain.Download
		_ = database.Get(r.Context(), a.Store.Read(), "downloads", id, &d)
		if d.ExternalID != "" {
			a.forgetTorrents(r.Context(), d.DownloaderID, []string{d.ExternalID})
		}
		v := DownloadViewOf(d)
		v.Label = PhaseLabels["removed"]
		if d.StateReason != nil {
			v.Text = upperFirst(*d.StateReason) + "."
		}
		// The Downloader's refusal, so the page can say it at once.
		webserver.WriteJSON(w, http.StatusOK, struct {
			DownloadView
			ClientNote string `json:"client_note,omitempty"`
		}{v, note})
	}
}

// importDownload asks again for the import of a finished download that still
// waits for it.
func (a *Web) importDownload(w http.ResponseWriter, r *http.Request) {
	id, ok := idIn(w, r, "download")
	if !ok {
		return
	}
	err := a.Manager().ImportAgain(r.Context(), id, actor(r))
	switch {
	case errors.Is(err, database.ErrNotFound), errors.Is(err, sql.ErrNoRows):
		webserver.Fail(w, http.StatusNotFound, "no such download")
	case errors.Is(err, ErrNotWaiting):
		webserver.Fail(w, http.StatusConflict, err.Error())
	case err != nil:
		webserver.Internal(w, "api", err)
	default:
		w.WriteHeader(http.StatusAccepted)
	}
}

// DecisionView is one movie waiting for a person on Needs a decision.
type DecisionView struct {
	SlotID      int64     `json:"slot_id"`
	TitleID     int64     `json:"title_id"`
	Title       string    `json:"title"`
	Year        *int      `json:"year,omitempty"`
	Version     string    `json:"version"`
	Versions    int       `json:"versions"` // how many Versions the Title has
	Why         string    `json:"why"`      // ran_out, conflict, not_replaced, not_sent, link_refused, taken
	Reason      string    `json:"reason"`
	ReleaseID   *int64    `json:"release_id,omitempty"`
	ReleaseName string    `json:"release_name,omitempty"`
	HasCopy     bool      `json:"has_copy"`
	Since       time.Time `json:"since"`
	// Shows: the episode, and the pack it came in (the other episodes of
	// the same download that also wait for a decision).
	// Artists: Episode is the track, Pack every track of the album
	// download, and Album names the album.
	Kind        string        `json:"kind"` // movie, show, artist
	Episode     *EpisodeLine  `json:"episode,omitempty"`
	Album       string        `json:"album,omitempty"`
	DownloadID  *int64        `json:"download_id,omitempty"`
	PackSlotIDs []int64       `json:"pack_slot_ids,omitempty"`
	Pack        []EpisodeLine `json:"pack,omitempty"` // every episode of that download
	// ImportFile names the one video a movie's finished download holds:
	// Review can import it into this movie (the "import" action).
	ImportFile string `json:"import_file,omitempty"`
	// ImportFolder is the finished download's folder as HolyArrs sees it:
	// Review opens Import files by hand there to match the files.
	ImportFolder string `json:"import_folder,omitempty"`
	// Reading: Import again reads the download's folder right now.
	Reading bool `json:"reading,omitempty"`
}

// why sorts a Slot's last reason into the ways it ends up waiting.
func why(reason string) string {
	switch {
	case strings.Contains(reason, "not replaced (setting)"):
		return "not_replaced"
	case strings.HasPrefix(reason, "not sent: the Release link"):
		// The Release link led to an address HolyArrs may not reach.
		return "link_refused"
	case strings.HasPrefix(reason, "not sent:"):
		// No Downloader takes the title's tags.
		return "not_sent"
	case strings.HasPrefix(reason, "nothing could be placed"), strings.Contains(reason, "import"),
		strings.Contains(reason, "could not be placed"), strings.HasPrefix(reason, "nothing in the download could be matched"), strings.HasPrefix(reason, "no file in the download matched"),
		// The unpack reasons (importer/unpack.go): the import could not finish, Releases did not run out.
		strings.Contains(reason, "RAR file"), strings.Contains(reason, "archive file"), strings.HasPrefix(reason, "Packed in "), strings.HasPrefix(reason, "Not enough free space to unpack"),
		// The file was refused in place of the copy, or its edition was not settled (importer/upgrade.go, copyfacts.go).
		strings.Contains(reason, "copy in place"), strings.HasPrefix(reason, "edition "):
		return "conflict"
	}
	return "ran_out"
}

// decisions lists the Slots that wait for a decision (one Slot when slotID
// is set), newest first. The whole list costs a fixed number of reads however
// long it is: the layout asks for it on every page, so a read per row made
// every page slow in a large Library.
func (a *Web) decisions(ctx context.Context, slotID int64) ([]DecisionView, error) {
	db := a.Store.Read()
	only, args := "", []any{}
	if slotID > 0 {
		only = " AND s.id = ?"
		args = append(args, slotID, slotID)
	}
	// last is the newest download of each waiting Slot, found in one pass
	// over downloads instead of one pass per Slot.
	rows, err := db.QueryContext(ctx, `WITH nd AS (SELECT s.id FROM slots s WHERE s.state = 'needs_a_decision'`+only+`),
		last AS (SELECT j.value AS slot_id, max(d.id) AS download_id FROM downloads d, json_each(d.slot_ids) j
			WHERE j.value IN (SELECT id FROM nd) GROUP BY j.value)
		SELECT s.id, t.id, t.name, t.year, v.label, (SELECT count(*) FROM versions v2 WHERE v2.title_id = t.id),
		COALESCE(s.last_reason, ''), s.file_id IS NOT NULL, s.updated_at, ld.release_id, t.kind, last.download_id,
		COALESCE(r.name, ''), COALESCE(p.name, '')
		FROM slots s JOIN items i ON i.id = s.item_id JOIN titles t ON t.id = i.title_id JOIN versions v ON v.id = s.version_id
		LEFT JOIN last ON last.slot_id = s.id LEFT JOIN downloads ld ON ld.id = last.download_id
		LEFT JOIN releases r ON r.id = ld.release_id LEFT JOIN parts p ON p.id = i.part_id
		WHERE s.state = 'needs_a_decision' AND t.state != 'removed'`+only+` ORDER BY s.updated_at DESC, s.id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DecisionView{}
	for rows.Next() {
		var dv DecisionView
		var year, rel, dl sql.NullInt64
		var since, album string
		if err := rows.Scan(&dv.SlotID, &dv.TitleID, &dv.Title, &year, &dv.Version, &dv.Versions, &dv.Reason, &dv.HasCopy, &since, &rel, &dv.Kind, &dl,
			&dv.ReleaseName, &album); err != nil {
			return nil, err
		}
		if dl.Valid {
			dv.DownloadID = &dl.Int64
		}
		if year.Valid {
			y := int(year.Int64)
			dv.Year = &y
		}
		if rel.Valid {
			dv.ReleaseID = &rel.Int64
		}
		if dv.Kind == "artist" {
			dv.Album = album
		}
		dv.Since, _ = time.Parse(database.TimeLayout, since)
		dv.Why = why(dv.Reason)
		out = append(out, dv)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// The downloads and the episode lines of every row are read together.
	var dlIDs []int64
	for i := range out {
		if out[i].DownloadID != nil {
			dlIDs = append(dlIDs, *out[i].DownloadID)
		}
	}
	dls := downloadsByID(ctx, db, dlIDs)
	var lineIDs []int64
	for i := range out {
		if out[i].Kind != "show" && out[i].Kind != "artist" {
			continue
		}
		lineIDs = append(lineIDs, out[i].SlotID)
		if out[i].DownloadID != nil {
			if d, ok := dls[*out[i].DownloadID]; ok && len(d.SlotIDs) > 1 {
				lineIDs = append(lineIDs, d.SlotIDs...)
			}
		}
	}
	lines := episodeLineMap(ctx, db, lineIDs)
	type review struct{ file, folder string }
	reviews := map[int64]*review{} // one disk check per download, not per Slot
	for i := range out {
		if out[i].Kind == "show" || out[i].Kind == "artist" {
			if e, ok := lines[out[i].SlotID]; ok {
				out[i].Episode = &e
			}
			if out[i].DownloadID != nil {
				if d, ok := dls[*out[i].DownloadID]; ok && len(d.SlotIDs) > 1 {
					out[i].Pack = pickLines(lines, d.SlotIDs)
					for _, e := range out[i].Pack {
						if e.State == string(mediafiles.SlotNeedsADecision) {
							out[i].PackSlotIDs = append(out[i].PackSlotIDs, e.SlotID)
						}
					}
				}
			}
		}
		// Only a finished download has files to review; the importer checks the
		// same, so the others are not asked at all.
		if out[i].DownloadID != nil {
			if d, ok := dls[*out[i].DownloadID]; ok && (d.State == "finished" || d.State == "seeding") {
				rv := reviews[d.ID]
				if rv == nil {
					rv = &review{folder: a.importer().ReviewFolder(ctx, d.ID)}
					if out[i].Kind == "movie" {
						if f := a.importer().ReviewFile(ctx, d.ID); f != "" {
							rv.file = filepath.Base(f)
						}
					}
					reviews[d.ID] = rv
				}
				out[i].ImportFolder = rv.folder
				if out[i].Kind == "movie" {
					out[i].ImportFile = rv.file
				}
			}
			out[i].Reading = a.Manager().Rereading(*out[i].DownloadID)
		}
		if out[i].Reason != "" {
			out[i].Reason = upperFirst(out[i].Reason) + "."
		}
	}
	return out, nil
}

// downloadsByID reads the downloads with these ids in a few reads; one that
// cannot be read is left out.
func downloadsByID(ctx context.Context, db *sql.DB, ids []int64) map[int64]domain.Download {
	out := map[int64]domain.Download{}
	for chunk := range slices.Chunk(ids, 500) {
		marks, args := strings.Repeat(",?", len(chunk))[1:], make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		ds, err := database.Select[domain.Download](ctx, db, "downloads", "id IN ("+marks+")", args...)
		if err != nil {
			continue
		}
		for _, d := range ds {
			out[d.ID] = d
		}
	}
	return out
}

func (a *Web) needsDecision(w http.ResponseWriter, r *http.Request) {
	list, err := a.decisions(r.Context(), 0)
	if err != nil {
		webserver.Internal(w, "api", err)
		return
	}
	webserver.WriteJSON(w, http.StatusOK, map[string]any{"items": list, "count": len(list)})
}

// decide runs a person's choice for a Slot that needs a decision:
// search (search again by hand), turn_down (turn its last Release down,
// then search again) or stop (stop looking: Keep up to date off for that
// Version, the Slot rests). Choosing a Release by hand and trying another
// Version happen on the Movie page.
// errMovedOn is a Slot that left Needs a decision while a choice was made.
var errMovedOn = errors.New("the slot no longer needs a decision")

// BeforeDecide lets tests change a Slot between the read and the write.
var BeforeDecide func()

func (a *Web) decide(w http.ResponseWriter, r *http.Request) {
	id, ok := idIn(w, r, "slot")
	if !ok {
		return
	}
	var in struct {
		Action  string `json:"action"`
		Pack    bool   `json:"pack"`
		Replace bool   `json:"replace"`
	}
	if !webserver.Decode(w, r, &in, `body is {"action": "search" | "turn_down" | "stop" | "import", "pack": bool (optional, for every episode of the same download), "replace": bool (import only)}`) {
		return
	}
	list, err := a.decisions(r.Context(), id)
	if err != nil {
		webserver.Internal(w, "api", err)
		return
	}
	if len(list) == 0 {
		webserver.Fail(w, http.StatusNotFound, "this movie or episode does not need a decision")
		return
	}
	dv := list[0]
	noun := "movie"
	many := dv.Kind == "show" || dv.Kind == "artist" // one Slot of many in a download
	switch dv.Kind {
	case "show":
		noun = "episode"
	case "artist":
		noun = "track"
	}
	targets := []DecisionView{dv}
	if in.Pack {
		if !many || len(dv.PackSlotIDs) == 0 {
			webserver.Fail(w, http.StatusBadRequest, "this "+noun+" did not come in a pack")
			return
		}
		targets = nil
		for _, sid := range dv.PackSlotIDs {
			if l, err := a.decisions(r.Context(), sid); err == nil && len(l) == 1 {
				targets = append(targets, l[0])
			}
		}
	}
	if BeforeDecide != nil {
		BeforeDecide()
	}
	by := actor(r)
	who := "someone"
	if p, ok := webserver.From(r.Context()); ok {
		who = p.Name
	}
	switch in.Action {
	case "import":
		a.importReviewFile(w, r, dv, in.Pack, in.Replace)
	case "turn_down":
		if dv.ReleaseID == nil {
			webserver.Fail(w, http.StatusConflict, "there is no Release to turn down for this "+noun)
			return
		}
		if a.Queue == nil {
			webserver.Fail(w, http.StatusServiceUnavailable, "searches are not running")
			return
		}
		reason := "turned down by " + who + " on Needs a decision"
		// An episode turns the Release down for that episode only; a pack
		// (or a movie) for everything it holds.
		if many && !in.Pack {
			err = a.searcher().TurnDownFor(r.Context(), *dv.ReleaseID, dv.SlotID, reason, by)
		} else {
			err = a.searcher().TurnDown(r.Context(), *dv.ReleaseID, reason, by)
		}
		if err != nil {
			sources.SearchError(w, err)
			return
		}
		type movedSlot struct {
			id    int64
			state mediafiles.SlotState
		}
		var moved []movedSlot
		err = a.Store.Write(r.Context(), func(tx *sql.Tx) error {
			moved = nil
			for _, t := range targets {
				var state string
				if err := tx.QueryRow("SELECT state FROM slots WHERE id = ?", t.SlotID).Scan(&state); err != nil {
					return err
				}
				if state != string(mediafiles.SlotNeedsADecision) {
					continue
				}
				if err := mediafiles.ClearSlotDownloadTx(tx, t.SlotID); err != nil {
					return err
				}
				st := mediafiles.SlotWanted
				if t.HasCopy {
					st = mediafiles.SlotBetterWanted
				}
				if err := mediafiles.MoveSlotTx(tx, t.SlotID, st, reason, by); err != nil {
					return err
				}
				moved = append(moved, movedSlot{id: t.SlotID, state: st})
			}
			if in.Pack && dv.DownloadID != nil {
				if _, err := tx.Exec("UPDATE downloads SET pack_files = NULL WHERE id = ? AND pack_files IS NOT NULL", *dv.DownloadID); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			webserver.Internal(w, "api", err)
			return
		}
		if a.Live != nil {
			for _, m := range moved {
				a.Live.Publish("slot", map[string]any{"id": m.id, "state": m.state, "reason": reason})
			}
		}
		fallthrough
	case "search":
		if a.Queue == nil {
			webserver.Fail(w, http.StatusServiceUnavailable, "searches are not running")
			return
		}
		var queued []int64
		enqueueSlot := func(sid int64) error {
			jid, _, err := a.Queue.Enqueue(r.Context(), jobs.KindSearchSlot, &domain.Subject{Type: "slot", ID: sid}, map[string]any{"by_hand": true},
				"hand_search_slot:"+itoa(sid), by)
			if err == nil {
				queued = append(queued, jid)
			}
			return err
		}
		if in.Pack && (dv.Kind == "show" || dv.Kind == "artist") {
			type groupKey struct {
				partID    int64
				versionID int64
			}
			type group struct {
				key   groupKey
				slots []int64
			}
			var order []groupKey
			groups := map[groupKey]*group{}
			var ungrouped []int64
			for _, t := range targets {
				verID, partID, _, _, err := mediafiles.SlotRetryFacts(r.Context(), a.Store.Read(), t.SlotID)
				if err != nil {
					webserver.Internal(w, "api", err)
					return
				}
				if !partID.Valid {
					ungrouped = append(ungrouped, t.SlotID)
					continue
				}
				k := groupKey{partID: partID.Int64, versionID: verID}
				g, ok := groups[k]
				if !ok {
					g = &group{key: k}
					groups[k] = g
					order = append(order, k)
				}
				g.slots = append(g.slots, t.SlotID)
			}
			for _, k := range order {
				g := groups[k]
				switch dv.Kind {
				case "show":
					if len(g.slots) > 1 {
						jid, _, err := a.Queue.Enqueue(r.Context(), jobs.KindSearchSeason, &domain.Subject{Type: "part", ID: k.partID},
							map[string]any{"version_id": k.versionID, "by_hand": true}, fmt.Sprintf("hand_search_season:%d:%d", k.partID, k.versionID), by)
						if err != nil {
							webserver.Internal(w, "api", err)
							return
						}
						queued = append(queued, jid)
					} else if err := enqueueSlot(g.slots[0]); err != nil {
						webserver.Internal(w, "api", err)
						return
					}
				case "artist":
					jid, _, err := a.Queue.Enqueue(r.Context(), jobs.KindSearchAlbum, &domain.Subject{Type: "part", ID: k.partID},
						map[string]any{"version_id": k.versionID, "by_hand": true}, fmt.Sprintf("hand_search_album:%d:%d", k.partID, k.versionID), by)
					if err != nil {
						webserver.Internal(w, "api", err)
						return
					}
					queued = append(queued, jid)
				}
			}
			for _, sid := range ungrouped {
				if err := enqueueSlot(sid); err != nil {
					webserver.Internal(w, "api", err)
					return
				}
			}
		} else {
			for _, t := range targets {
				if err := enqueueSlot(t.SlotID); err != nil {
					webserver.Internal(w, "api", err)
					return
				}
			}
		}
		webserver.WriteJSON(w, http.StatusAccepted, map[string]any{"job_id": queued[0], "job_ids": queued, "action": in.Action})
	case "stop":
		to := mediafiles.SlotWanted
		if dv.HasCopy {
			to = mediafiles.SlotBetterWanted
		}
		reason := "stopped looking (" + who + ")"
		var stopped []DecisionView
		err := a.Store.Write(r.Context(), func(tx *sql.Tx) error {
			stopped = nil
			for _, t := range targets {
				// The Slot may have moved on since it was read: only one that
				// still needs a decision stops, and nothing is written otherwise.
				var state string
				if err := tx.QueryRow("SELECT state FROM slots WHERE id = ?", t.SlotID).Scan(&state); err != nil {
					return err
				}
				if state != string(mediafiles.SlotNeedsADecision) {
					if t.SlotID == id {
						return errMovedOn
					}
					continue
				}
				// A movie stops for its Version; an episode for itself, so
				// the rest of the show keeps going.
				q := "UPDATE versions SET keep_up_to_date = 0, updated_at = ? WHERE id = (SELECT version_id FROM slots WHERE id = ?)"
				if t.Kind == "show" || t.Kind == "artist" {
					q = "UPDATE items SET keep_up_to_date = 0, updated_at = ? WHERE id = (SELECT item_id FROM slots WHERE id = ?)"
				}
				if _, err := tx.Exec(q, database.FormatTime(time.Now()), t.SlotID); err != nil {
					return err
				}
				st := mediafiles.SlotWanted
				if t.HasCopy {
					st = mediafiles.SlotBetterWanted
				}
				if err := mediafiles.MoveSlotTx(tx, t.SlotID, st, reason, by); err != nil {
					return err
				}
				stopped = append(stopped, t)
			}
			return nil
		})
		if errors.Is(err, errMovedOn) {
			webserver.Fail(w, http.StatusConflict, "this "+noun+" no longer needs a decision")
			return
		}
		if err != nil {
			webserver.Internal(w, "api", err)
			return
		}
		for _, t := range stopped {
			tid, sid := t.TitleID, t.SlotID
			name := t.Title + " (" + t.Version + ")"
			if t.Episode != nil {
				name = t.Title + " " + t.Episode.Label
			}
			e := &domain.Event{Actor: by, Kind: "stopped_looking", Summary: "Stopped looking for " + name, TitleID: &tid, SlotID: &sid}
			if a.Store.AddEvent(r.Context(), e) == nil && a.Live != nil {
				a.Live.Publish("event", e)
			}
			st := mediafiles.SlotWanted
			if t.HasCopy {
				st = mediafiles.SlotBetterWanted
			}
			if a.Live != nil {
				a.Live.Publish("slot", map[string]any{"id": sid, "state": st, "reason": reason})
			}
		}
		webserver.WriteJSON(w, http.StatusOK, map[string]any{"action": "stop", "state": to, "stopped": len(stopped)})
	default:
		webserver.Fail(w, http.StatusBadRequest, `action is "search", "turn_down", "stop" or "import"`)
	}
}

// importer places files the way the import job does, for Review's Import this file.
func (a *Web) importer() *importer.Importer {
	im := &importer.Importer{Store: a.Store, OnImported: a.Manager().ClearFinished}
	if a.Live != nil {
		im.Live = a.Live
	}
	return im
}

// importReviewFile imports a movie's one downloaded video into the movie it
// was grabbed for: the person decided the file is that movie. A movie that
// has a file keeps it unless replace is sent; the old file then goes to the
// holding folder (Recently deleted).
func (a *Web) importReviewFile(w http.ResponseWriter, r *http.Request, dv DecisionView, pack, replace bool) {
	switch {
	case dv.Kind != "movie" || pack:
		webserver.Fail(w, http.StatusBadRequest, "only a movie's download is imported from here; use Import files by hand")
		return
	case dv.DownloadID == nil || dv.ImportFile == "":
		webserver.Fail(w, http.StatusConflict, "the download has no single video to import; use Import files by hand")
		return
	case dv.HasCopy && !replace:
		webserver.Fail(w, http.StatusConflict, "this movie already has a file; send replace to put this one in its place")
		return
	}
	res, err := a.importer().ImportReviewFile(r.Context(), dv.SlotID, *dv.DownloadID, replace, actor(r))
	if err != nil {
		webserver.Internal(w, "api", err)
		return
	}
	if res.Outcome != "placed" {
		webserver.Fail(w, http.StatusConflict, res.Error)
		return
	}
	webserver.WriteJSON(w, http.StatusOK, map[string]any{"action": "import", "result": res})
}

// clearNow tries every imported download still waiting to be cleared from
// its Downloader at once, and answers how many still wait.
func (a *Web) clearNow(w http.ResponseWriter, r *http.Request) {
	webserver.WriteJSON(w, http.StatusOK, map[string]int{"left": a.Manager().ClearNow(r.Context())})
}

// EpisodeLine is one episode (or track) a download covers, with where its
// Slot is. A track carries its disc in Season and its number in Episode.
type EpisodeLine struct {
	SlotID  int64  `json:"slot_id"`
	Season  int    `json:"season"`
	Episode int    `json:"episode"`
	Label   string `json:"label"` // S01E02; a track: Track 02, Disc 2 track 03
	Name    string `json:"name,omitempty"`
	State   string `json:"state"`
	Track   bool   `json:"track,omitempty"`
}

// episodeLines reads the episodes of a show download's Slots, in order.
func episodeLines(ctx context.Context, db *sql.DB, slotIDs []int64) []EpisodeLine {
	return pickLines(episodeLineMap(ctx, db, slotIDs), slotIDs)
}

// pickLines picks the lines of these Slots, in season and episode order; a
// Slot with no line is left out.
func pickLines(lines map[int64]EpisodeLine, slotIDs []int64) []EpisodeLine {
	out := []EpisodeLine{}
	for _, id := range slotIDs {
		if e, ok := lines[id]; ok {
			out = append(out, e)
		}
	}
	SortLines(out)
	return out
}

// episodeLineMap reads the episode (or track) line of each Slot in a few
// reads, however many Slots there are.
func episodeLineMap(ctx context.Context, db *sql.DB, slotIDs []int64) map[int64]EpisodeLine {
	out := map[int64]EpisodeLine{}
	for chunk := range slices.Chunk(slotIDs, 500) {
		marks, args := strings.Repeat(",?", len(chunk))[1:], make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := db.QueryContext(ctx, `SELECT s.id, s.state, i.kind, CASE i.kind WHEN 'track' THEN COALESCE(json_extract(i.numbers, '$.disc'), 1) ELSE json_extract(i.numbers, '$.season') END,
			CASE i.kind WHEN 'track' THEN json_extract(i.numbers, '$.track') ELSE json_extract(i.numbers, '$.episode') END, i.name
			FROM slots s JOIN items i ON i.id = s.item_id WHERE s.id IN (`+marks+`)`, args...)
		if err != nil {
			continue
		}
		for rows.Next() {
			var e EpisodeLine
			var season, episode sql.NullInt64
			var name sql.NullString
			var kind string
			if rows.Scan(&e.SlotID, &e.State, &kind, &season, &episode, &name) != nil {
				continue
			}
			e.Season, e.Episode, e.Name = int(season.Int64), int(episode.Int64), name.String
			labelLine(&e, kind)
			out[e.SlotID] = e
		}
		_ = rows.Close()
	}
	return out
}

// labelLine names an episode line: S01E02, or for a track Track 02 or
// Disc 2 track 03.
func labelLine(e *EpisodeLine, kind string) {
	e.Label = fmt.Sprintf("S%02dE%02d", e.Season, e.Episode)
	if kind == "track" {
		e.Track = true
		e.Label = fmt.Sprintf("Track %02d", e.Episode)
		if e.Season > 1 {
			e.Label = fmt.Sprintf("Disc %d track %02d", e.Season, e.Episode)
		}
	}
}

// SortLines puts episode lines in season and episode order.
func SortLines(out []EpisodeLine) {
	sort.Slice(out, func(i, j int) bool {
		if out[i].Season != out[j].Season {
			return out[i].Season < out[j].Season
		}
		return out[i].Episode < out[j].Episode
	})
}
