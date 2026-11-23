package subtitles

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/mediafiles"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/sites"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/subtext"
)

// SearchOptions changes how one search behaves.
type SearchOptions struct {
	// Better also searches again for wanted languages whose saved subtitle
	// scored under 99 and was saved within BetterDays days (0 means any age).
	Better     bool
	BetterDays int
	// Show only lists the candidates for a person to pick; nothing is saved.
	Show bool
	// NoTranslate never queues a translation; the search a failed translation
	// falls back to sets it.
	NoTranslate bool
}

// Outcome is what a search did for one wanted language.
type Outcome struct {
	Want    Want     `json:"want"`
	Result  string   `json:"result"` // saved, upgraded, none_good_enough, failed, kept or translating
	Path    string   `json:"path,omitempty"`
	Score   int      `json:"score,omitempty"`
	Site    string   `json:"site,omitempty"`
	Reason  string   `json:"reason,omitempty"`
	Made    string   `json:"made,omitempty"`    // a machine-made file stood in, see Subtitle.Made
	Choices []Scored `json:"choices,omitempty"` // filled only for Show searches
	Covered []Have   `json:"covered,omitempty"` // Show searches: what the File already has that meets this want
}

// SearchResult is the outcome of searching one File.
type SearchResult struct {
	FileID   int64     `json:"file_id"`
	Status   Status    `json:"status"`
	Outcomes []Outcome `json:"outcomes"`
	Asked    []string  `json:"sites_asked"`
}

// target is one language a search looks for, with the score it must beat.
type target struct {
	want  Want
	beat  int       // a candidate must score above this; -1 means anything over the pass mark
	old   *Subtitle // the saved subtitle a better one would replace
	isNew bool
}

// Search looks on every usable site for the File's missing subtitles, plus
// improvable ones when Better is set, and saves the best one over the pass mark.
func (s *Service) Search(ctx context.Context, fileID int64, opt SearchOptions, by domain.Actor) (SearchResult, error) {
	res := SearchResult{FileID: fileID, Outcomes: []Outcome{}, Asked: []string{}}
	fc, err := s.load(ctx, fileID)
	if err != nil {
		return res, err
	}
	if fc.look {
		return SearchResult{}, mediafiles.ErrLookOnly
	}
	st, err := s.look(ctx, fc, by)
	if err != nil {
		return res, err
	}
	res.Status = st
	var skipped map[wishKey]bool
	if !opt.Show {
		skipped, _ = skippedWishesForFile(ctx, s.Store.Read(), fileID)
	}
	var targets []target
	for _, m := range st.Missing {
		if skipped[wishKey{language: m.Language, forced: m.Forced}] {
			continue
		}
		targets = append(targets, target{want: m, beat: -1, isNew: true})
	}
	if opt.Better || opt.Show {
		// A person choosing by hand may swap their own earlier pick too; timed
		// better searches leave hand picks alone.
		// A subtitle whose timing was moved by hand is kept the same way.
		origins := "origin = 'fetched' AND json_extract(sync, '$.from_subtitle_id') IS NULL"
		if opt.Show {
			origins = "origin IN ('fetched', 'by_hand')"
		}
		saved, err := database.Select[Subtitle](ctx, s.Store.Read(), "subtitles",
			"file_id = ? AND place = 'beside' AND state = 'present' AND "+origins+" AND score IS NOT NULL", fileID)
		if err != nil {
			return res, err
		}
		for i := range saved {
			sv := &saved[i]
			// A machine-made subtitle is looked past at any age, and any file made
			// by a person that passes replaces it.
			stop := sv.Made != nil
			if !opt.Show && !stop && (*sv.Score >= 99 || (opt.BetterDays > 0 && sv.SavedAt != nil && sv.SavedAt.Before(now().AddDate(0, 0, -opt.BetterDays)))) {
				continue
			}
			for _, w := range st.Wish.Wants {
				if meets(w, Have{Language: sv.Language, Forced: sv.Forced, HearingImpaired: sv.HearingImpaired}) {
					if opt.Show && hasTarget(targets, w) {
						break // a second saved file for the same want would list the same results twice
					}
					t := target{want: w, beat: *sv.Score, old: sv}
					if stop {
						t.beat = -1
					}
					targets = append(targets, t)
					break
				}
			}
		}
		if opt.Show {
			// A person choosing by hand sees results for every wanted language,
			// even one a built-in or found file already covers.
			for _, w := range st.Wish.Wants {
				if !hasTarget(targets, w) {
					targets = append(targets, target{want: w, beat: -1})
				}
			}
		}
	}
	if len(targets) == 0 {
		return res, nil
	}
	set := ReadSettings(ctx, s.Store.Read())
	pass := set.PassMark(fc.item.Kind)
	v := fc.videoFacts()
	q := sites.Query{Kind: fc.item.Kind, Title: v.Title, Year: v.Year, Season: v.Season, Episode: v.Episode, ReleaseName: v.ReleaseName,
		IMDb: fc.title.SourceIDs["imdb"], TMDB: fc.title.SourceIDs["tmdb"], TVDB: fc.title.SourceIDs["tvdb"]}
	if _, err := os.Stat(fc.video()); err == nil {
		q.File = fc.video() // for sites that hash the file their own way
	}
	if h, err := s.videoHash(ctx, fc, set); err == nil && h != "" {
		q.Hash = h // hash-matching sites use it and the rest ignore it
		if st, err := os.Stat(fc.video()); err == nil {
			q.Size = st.Size()
		}
	}
	sc := &searchCtx{fc: fc, status: st, set: set, opt: opt, pass: pass, video: v, bySite: map[string]SiteRow{}, by: by}
	seenLang := map[string]bool{}
	ask := func(l string) {
		if !seenLang[l] {
			seenLang[l] = true
			q.Languages = append(q.Languages, l)
		}
	}
	for _, t := range targets {
		ask(t.want.Language)
		if t.isNew && t.want.TranslateFrom != "" && !opt.Show && !opt.NoTranslate {
			if !sc.translator.Ready() {
				sc.translator = s.Translator(ctx)
			}
			// With any language there is no one language to ask the sites for.
			if sc.translator.Ready() && t.want.TranslateFrom != FromAny {
				ask(t.want.TranslateFrom) // a source to translate from, in the same call
			}
		}
	}
	turned := s.turnedDown(ctx, fileID)
	sc.turned = turned

	// Each usable site is asked once for all languages to keep traffic low.
	rows, err := usable(ctx, s.Store.Read(), now())
	if err != nil {
		return res, err
	}
	var all []Scored
	bySite := sc.bySite
	for order, r := range rows {
		k, _ := sites.KindOf(r.Kind)
		if (fc.item.Kind == "movie" && !k.Movies) || (fc.item.Kind == "episode" && !k.Shows) {
			continue
		}
		site, err := s.client(ctx, r)
		if err != nil {
			continue
		}
		res.Asked = append(res.Asked, r.Name)
		cands, err := site.Search(ctx, q)
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		if err != nil {
			s.pause(ctx, r, err)
			continue
		}
		s.recovered(ctx, r)
		bySite[r.Name] = r
		for _, c := range cands[:min(len(cands), MaxCandidatesPerSite)] {
			if c.MachineMade != "" && !r.MachineMade {
				continue // the site's machine-made switch is off
			}
			c.Site = r.Name
			all = append(all, Scored{Candidate: c, Priority: r.Priority, Order: order})
		}
	}
	sc.all = all

	for _, t := range targets {
		var choices []Scored
		for _, c := range all {
			if !LangMatches(t.want.Language, c.Language) || turned[c.Site+"\x00"+c.Key] {
				continue
			}
			c.Score = ScoreCandidate(v, t.want, c.Candidate)
			choices = append(choices, c)
		}
		Rank(choices)
		out := Outcome{Want: t.want}
		if opt.Show {
			out.Result, out.Choices = "shown", choices
			for _, h := range st.Haves {
				if meets(t.want, h) {
					out.Covered = append(out.Covered, h)
				}
			}
			res.Outcomes = append(res.Outcomes, out)
			continue
		}
		out.Result = "none_good_enough"
		for _, c := range choices {
			if c.MachineMade != "" {
				continue // used only when nothing made by a person passes
			}
			if c.Score.Refused != "" || c.Score.Total < pass || c.Score.Total <= t.beat {
				break // the list is ranked, so nothing after this is better
			}
			p, err := s.fetchAndSave(ctx, fc, t, c, bySite[c.Site], set, by, true)
			if err != nil {
				out.Reason = err.Error()
				continue
			}
			lang := t.want.Language
			if LangMatches(lang, c.Language) {
				lang = c.Language
			}
			sc.status.Haves = append(sc.status.Haves, Have{Language: lang, Forced: t.want.Forced, HearingImpaired: c.HearingImpaired, Place: "beside", Path: p})
			out.Result, out.Path, out.Score, out.Site, out.Reason = "saved", p, c.Score.Total, c.Site, ""
			if t.old != nil {
				out.Result = "upgraded"
			}
			break
		}
		if t.isNew && out.Result == "none_good_enough" {
			s.stopgap(ctx, sc, t, choices, &out)
		}
		if t.old != nil && out.Result == "none_good_enough" {
			out.Result = "kept"
			if t.old.Made != nil {
				s.touchStopgap(ctx, t.old.ID)
			}
		}
		s.afterTarget(ctx, fc, t, &out, set, by)
		res.Outcomes = append(res.Outcomes, out)
	}
	// A machine-made subtitle no want asked about (a transcript in a language
	// nobody wants) was looked past too: without this its File would be due
	// for the weekly look on every run.
	if opt.Better {
		if rows, err := stopgapRows(ctx, s.Store.Read(), fileID); err == nil {
			for _, r := range rows {
				s.touchStopgap(ctx, r.ID)
			}
		}
	}
	return res, nil
}

// afterTarget records how a target ended: its wish row state and an Event.
func (s *Service) afterTarget(ctx context.Context, fc *fileCtx, t target, out *Outcome, set Settings, by domain.Actor) {
	titleID, fileID := fc.title.ID, fc.file.ID
	slotID := fc.file.SlotIDs[0]
	switch out.Result {
	case "saved", "upgraded":
		s.dropSatisfiedQueuedWork(ctx, fileID, t.want)
		kind, verb := EventSaved, "Saved"
		if out.Result == "upgraded" {
			kind, verb = EventUpgraded, "Saved a better"
		}
		s.event(ctx, &domain.Event{Actor: by, Kind: kind, TitleID: &titleID, SlotID: &slotID,
			Summary: fmt.Sprintf("%s %s subtitle from %s (score %d)", verb, fmtWant(t.want), out.Site, out.Score),
			Data:    map[string]any{"file_id": fileID, "language": t.want.Language, "path": out.Path, "score": out.Score, "site": out.Site}, GroupKey: "subtitles:" + itoa(fileID)})
	case "none_good_enough", "translating", "transcribing":
		next := now().Add(time.Duration(set.SearchHours) * time.Hour)
		_ = s.Store.Write(ctx, func(tx *sql.Tx) error {
			var first sql.NullString
			_ = tx.QueryRowContext(ctx, "SELECT first_tried_at FROM subtitles WHERE file_id = ? AND place = 'wish' AND language = ? AND forced = ?",
				fileID, t.want.Language, t.want.Forced).Scan(&first)
			if set.Adaptive && first.Valid {
				if ft, err := time.Parse(database.TimeLayout, first.String); err == nil && now().Sub(ft) > 21*24*time.Hour {
					next = now().Add(7 * 24 * time.Hour)
				}
			}
			_, err := tx.ExecContext(ctx, `UPDATE subtitles SET state = 'none_good_enough', tries = tries + 1, first_tried_at = COALESCE(first_tried_at, ?),
				last_tried_at = ?, next_try_at = ?, updated_at = ? WHERE file_id = ? AND place = 'wish' AND language = ? AND forced = ?`,
				database.FormatTime(now()), database.FormatTime(now()), database.FormatTime(next), database.FormatTime(now()), fileID, t.want.Language, t.want.Forced)
			return err
		})
		if out.Result == "translating" || out.Result == "transcribing" {
			return // the translation or speech Job writes its own Event
		}
		s.event(ctx, &domain.Event{Actor: by, Kind: EventNoneGood, TitleID: &titleID, SlotID: &slotID,
			Summary: fmt.Sprintf("No %s subtitle reached the Pass mark", fmtWant(t.want)),
			Data:    map[string]any{"file_id": fileID, "language": t.want.Language, "reason": out.Reason, "next_try_at": next}, GroupKey: "subtitles:" + itoa(fileID)})
	}
}

// fetchAndSave downloads a candidate, checks it and saves it beside the video,
// holding any replaced subtitle aside. It returns the saved path.
func (s *Service) fetchAndSave(ctx context.Context, fc *fileCtx, t target, c Scored, r SiteRow, set Settings, by domain.Actor, checkIntegrity bool) (string, error) {
	site, err := s.client(ctx, r)
	if err != nil {
		return "", err
	}
	if !s.countDownload(ctx, r) {
		return "", errors.New("the site's downloads for today are used up")
	}
	data, err := site.Download(ctx, c.Candidate)
	if err != nil {
		s.pause(ctx, r, err)
		return "", err
	}
	entries, err := subtext.Unpack(data, c.Key+"."+c.Format)
	if err != nil {
		s.failed(ctx, fc, t.want, c.Site, err, by)
		return "", err
	}
	e := pickEntry(entries)
	// The language picks the old encodings to try on a file that is not Unicode.
	text, err := subtext.CleanFor(e.Data, e.Format, cmp.Or(c.Language, t.want.Language))
	if err != nil {
		s.failed(ctx, fc, t.want, c.Site, err, by)
		return "", err
	}
	siteID := r.ID
	// The file keeps the language the site gave, so a pt-BR file found for a pt
	// wish is named and recorded as pt-BR.
	lang := t.want.Language
	if LangMatches(lang, c.Language) {
		lang = c.Language
	}
	row := Subtitle{FileID: fc.file.ID, Language: lang, Forced: t.want.Forced, HearingImpaired: c.HearingImpaired, Place: "beside",
		SiteName: &c.Site, SiteKey: &c.Key, Score: &c.Score.Total, ScoreParts: c.Score.Parts, Origin: "fetched", State: "present", Sync: map[string]any{}}
	if checkIntegrity {
		videoSec := fc.probedVideoSeconds()
		rep := subtext.CheckIntegrity(text, e.Format, videoSec, t.want.Forced)
		if !rep.OK {
			reason := rep.Reason
			if reason == "" {
				reason = string(rep.Issue)
			}
			ierr := errors.New(reason)
			s.failed(ctx, fc, t.want, c.Site, ierr, by)
			return "", ierr
		}
		if videoSec > 0 {
			state := "ok"
			checkedAt := now()
			cov := rep.Coverage
			lastMS := rep.LastMS
			row.IntegrityState = &state
			row.IntegrityCoverage = &cov
			row.IntegrityLastMS = &lastMS
			row.IntegrityVideoS = &videoSec
			row.IntegrityCheckedAt = &checkedAt
		}
	}
	if c.MachineMade != "" {
		// A machine-made file is marked as such and keeps a low score, so a
		// later search replaces it with a file made by a person.
		made, score := c.MachineMade, stopgapScore(c.MachineMade, c.Score.Total)
		row.Made, row.Score = &made, &score
	}
	if siteID != 0 {
		row.SiteID = &siteID
	}
	p, err := s.save(ctx, fc, &row, text, e.Format, set, t.old, by)
	if err != nil {
		s.failed(ctx, fc, t.want, c.Site, err, by)
	}
	return p, err
}

// pickEntry prefers an SRT entry and falls back to the first one.
func pickEntry(es []subtext.Entry) subtext.Entry {
	for _, e := range es {
		if e.Format == subtext.SRT {
			return e
		}
	}
	return es[0]
}

func (s *Service) failed(ctx context.Context, fc *fileCtx, w Want, site string, err error, by domain.Actor) {
	titleID, slotID, fileID := fc.title.ID, fc.file.SlotIDs[0], fc.file.ID
	// Disk errors carry full paths. Events and the live stream must only show
	// paths inside the Library folder.
	msg := err.Error()
	if lib := filepath.Clean(fc.lib); lib != "." && lib != "" {
		msg = strings.ReplaceAll(msg, lib+string(filepath.Separator), "")
		msg = strings.ReplaceAll(msg, lib, "the Library folder")
	}
	s.event(ctx, &domain.Event{Actor: by, Kind: EventFailed, TitleID: &titleID, SlotID: &slotID,
		Summary: fmt.Sprintf("A %s subtitle from %s could not be saved: %s", fmtWant(w), site, msg),
		Data:    map[string]any{"file_id": fileID, "language": w.Language, "site": site, "error": msg}, GroupKey: "subtitles:" + itoa(fileID)})
}

// SubtitleName builds a subtitle file name for a video in the form
// base.lang[.hi|.forced].format.
func SubtitleName(videoRel, lang string, hi, forced bool, hiSuffix, format string) string {
	base := strings.TrimSuffix(path.Base(videoRel), path.Ext(videoRel))
	name := base + "." + LanguageTag(lang)
	switch {
	case forced:
		name += ".forced"
	case hi:
		name += "." + hiSuffix
	}
	return path.Join(path.Dir(videoRel), name+"."+format)
}

// ErrNotOurs refuses to overwrite a subtitle file the app did not save, so a
// person's own file is never lost.
var ErrNotOurs = errors.New("a subtitle file HolyArrs did not save is at that name; it is left alone")

// save writes text beside the video under its language-tagged name, using the
// importer's safe-write rules. The path must stay inside the Library folder
// with links followed. The text goes to a temporary file that is synced and
// then renamed into place. A file already at the name is held aside in the
// holding folder if the app saved it; otherwise the save is refused.
func (s *Service) save(ctx context.Context, fc *fileCtx, row *Subtitle, text, format string, set Settings, old *Subtitle, by domain.Actor) (string, error) {
	rel := SubtitleName(fc.file.RelativePath, row.Language, row.HearingImpaired, row.Forced, set.HISuffix, format)
	return s.saveClean(ctx, fc, row, text, format, rel, set, old, by)
}

func (s *Service) turnedDown(ctx context.Context, fileID int64) map[string]bool {
	out := map[string]bool{}
	rows, err := s.Store.Read().QueryContext(ctx, "SELECT site_name, site_key FROM subtitle_turned_down WHERE file_id = ?", fileID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var a, b string
		if rows.Scan(&a, &b) == nil {
			out[a+"\x00"+b] = true
		}
	}
	return out
}

// hasTarget reports whether a target for w's language and forced flag is listed.
func hasTarget(targets []target, w Want) bool {
	for _, t := range targets {
		if t.want.Language == w.Language && t.want.Forced == w.Forced {
			return true
		}
	}
	return false
}
