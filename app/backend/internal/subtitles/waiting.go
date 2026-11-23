package subtitles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/jobs"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/webserver"
	"github.com/HolyArrs/holyarrs/app/backend/internal/mediafiles"
)

// WaitingJob describes one queued subtitle job with its queue execution position.
type WaitingJob struct {
	ID           int64     `json:"id"`
	Kind         string    `json:"kind"`
	State        string    `json:"state"`
	Priority     string    `json:"priority"`
	Position     int       `json:"position"`
	FileID       int64     `json:"file_id,omitempty"`
	TitleID      int64     `json:"title_id,omitempty"`
	Name         string    `json:"name,omitempty"`
	MediaKind    string    `json:"media_kind,omitempty"`
	Season       *int      `json:"season,omitempty"`
	Episode      *int      `json:"episode,omitempty"`
	RelativePath string    `json:"relative_path,omitempty"`
	Language     string    `json:"language,omitempty"`
	Forced       bool      `json:"forced,omitempty"`
	Model        string    `json:"model,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	RunAfter     time.Time `json:"run_after"`
}

// ListWaitingJobs returns queued subtitle jobs in execution order, filtered and paginated.
func (s *Service) ListWaitingJobs(ctx context.Context, query, kindFilter string, limit, offset int) ([]WaitingJob, int, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}

	db := s.Store.Read()
	qLower := strings.ToLower(strings.TrimSpace(query))
	kindTrim := strings.TrimSpace(kindFilter)

	var list []jobs.Job
	var total int
	var err error
	if qLower == "" && kindTrim == "" {
		list, total, err = jobs.ListQueued(ctx, db, overviewKinds, limit, offset)
		if err != nil {
			return nil, 0, err
		}
	} else {
		_, totalCount, cErr := jobs.ListQueued(ctx, db, overviewKinds, 0, 0)
		if cErr != nil {
			return nil, 0, cErr
		}
		if totalCount == 0 {
			return []WaitingJob{}, 0, nil
		}
		list, _, err = jobs.ListQueued(ctx, db, overviewKinds, totalCount, 0)
		if err != nil {
			return nil, 0, err
		}
	}

	summaries, err := mediafiles.GetFileSummariesByIDs(ctx, db, jobFileIDs(list))
	if err != nil {
		return nil, 0, err
	}

	toWaiting := func(idx int, j jobs.Job) WaitingJob {
		pos := idx + 1
		if qLower == "" && kindTrim == "" {
			pos = offset + idx + 1
		}
		wj := WaitingJob{
			ID:        j.ID,
			Kind:      j.Kind,
			State:     j.State,
			Priority:  j.Priority,
			Position:  pos,
			CreatedAt: j.CreatedAt,
			RunAfter:  j.RunAfter,
		}
		wj.Language, _ = j.Params["language"].(string)
		wj.Forced, _ = j.Params["forced"].(bool)
		if id, _ := j.Params["id"].(string); j.Kind == KindModelDownload && id != "" {
			wj.Model = id
			if m, ok := modelName(id); ok {
				wj.Model = m
			}
		}
		if j.Subject != nil && j.Subject.Type == "file" && j.Subject.ID > 0 {
			wj.FileID = j.Subject.ID
			if meta, exists := summaries[wj.FileID]; exists {
				wj.TitleID = meta.TitleID
				wj.Name = meta.Title
				wj.MediaKind = meta.MediaKind
				wj.Season = meta.Season
				wj.Episode = meta.Episode
				wj.RelativePath = meta.RelativePath
			}
		}
		return wj
	}

	if qLower == "" && kindTrim == "" {
		out := make([]WaitingJob, 0, len(list))
		for idx, j := range list {
			out = append(out, toWaiting(idx, j))
		}
		return out, total, nil
	}

	matched := make([]WaitingJob, 0, len(list))
	for idx, j := range list {
		if kindTrim != "" && j.Kind != kindTrim {
			continue
		}
		wj := toWaiting(idx, j)
		if qLower != "" {
			if !strings.Contains(strings.ToLower(wj.Name), qLower) &&
				!strings.Contains(strings.ToLower(wj.RelativePath), qLower) &&
				!strings.Contains(strings.ToLower(wj.Language), qLower) &&
				!strings.Contains(strings.ToLower(wj.Kind), qLower) &&
				!strings.Contains(strings.ToLower(wj.Model), qLower) {
				continue
			}
		}
		matched = append(matched, wj)
	}
	total = len(matched)
	if offset >= total {
		return []WaitingJob{}, total, nil
	}
	end := min(offset+limit, total)
	return matched[offset:end], total, nil
}

// BumpJob moves a queued subtitle job to the front of the waiting queue.
func (s *Service) BumpJob(ctx context.Context, id int64) (jobs.Job, error) {
	if s.Jobs == nil {
		return jobs.Job{}, errors.New("no job queue")
	}
	var j jobs.Job
	if err := database.Get(ctx, s.Store.Read(), "jobs", id, &j); err != nil {
		return jobs.Job{}, err
	}
	if !isSubtitleJobKind(j.Kind) {
		return jobs.Job{}, database.ErrNotFound
	}
	return s.Jobs.Promote(ctx, id)
}

// SkipJob drops a queued subtitle job cleanly and marks its matching open wish(es) as skipped.
func (s *Service) SkipJob(ctx context.Context, id int64, by domain.Actor) (int64, error) {
	if s.Jobs == nil {
		return 0, errors.New("no job queue")
	}
	var j jobs.Job
	if err := database.Get(ctx, s.Store.Read(), "jobs", id, &j); err != nil {
		return 0, err
	}
	if !isSubtitleJobKind(j.Kind) {
		return 0, database.ErrNotFound
	}
	if j.State != "queued" {
		return 0, fmt.Errorf("%w: %s", jobs.ErrNotQueued, j.State)
	}
	dropped, err := s.Jobs.DropQueued(ctx, id)
	if err != nil {
		return 0, err
	}
	if dropped.Kind == KindModelDownload {
		if mid, _ := dropped.Params["id"].(string); mid != "" {
			_ = s.CancelModelDownload(ctx, mid)
		}
	}
	var fileID int64
	if dropped.Subject != nil && dropped.Subject.Type == "file" && dropped.Subject.ID > 0 {
		fileID = dropped.Subject.ID
		langRaw, _ := dropped.Params["language"].(string)
		lang := NormLanguage(langRaw)
		forced, _ := dropped.Params["forced"].(bool)
		all := lang == ""
		if _, err := markWishesSkipped(ctx, s.Store, fileID, lang, forced, all, by); err != nil {
			return fileID, err
		}
		s.dropQueuedSubtitleJobsForTarget(ctx, fileID, lang, forced, all)
	}
	return fileID, nil
}

// SkipFile marks matching open wishes on fileID as skipped and drops matching queued jobs.
func (s *Service) SkipFile(ctx context.Context, fileID int64, language string, forced bool, by domain.Actor) (int, error) {
	if _, err := s.load(ctx, fileID); err != nil {
		return 0, err
	}
	all := language == ""
	count, err := markWishesSkipped(ctx, s.Store, fileID, language, forced, all, by)
	if err != nil {
		return 0, err
	}
	s.dropQueuedSubtitleJobsForTarget(ctx, fileID, language, forced, all)
	return count, nil
}

// UnskipFile restores skipped wishes on fileID back to wanted.
func (s *Service) UnskipFile(ctx context.Context, fileID int64, by domain.Actor) {
	_ = clearSkippedWishes(ctx, s.Store, fileID, "", false, true, by)
}

// UnskipLanguage restores a specific skipped wish on fileID back to wanted.
func (s *Service) UnskipLanguage(ctx context.Context, fileID int64, language string, forced bool, by domain.Actor) {
	if language == "" {
		return
	}
	_ = clearSkippedWishes(ctx, s.Store, fileID, language, forced, false, by)
}

func (s *Service) dropQueuedSubtitleJobsForTarget(ctx context.Context, fileID int64, lang string, forced bool, all bool) {
	if s.Jobs == nil || fileID <= 0 {
		return
	}
	db := s.Store.Read()
	targetKinds := []string{KindSearch, KindTranscribe, KindTranslate}
	_, total, err := jobs.ListQueued(ctx, db, targetKinds, 0, 0)
	if err != nil || total == 0 {
		return
	}
	queued, _, err := jobs.ListQueued(ctx, db, targetKinds, total, 0)
	if err != nil {
		return
	}
	hasOpen := false
	if !all {
		hasOpen, _ = hasOpenNonSkippedWish(ctx, db, fileID)
	}
	for _, oj := range queued {
		if oj.Subject == nil || oj.Subject.Type != "file" || oj.Subject.ID != fileID {
			continue
		}
		if all {
			_, _ = s.Jobs.DropQueued(ctx, oj.ID)
			continue
		}
		switch oj.Kind {
		case KindTranslate:
			ojLang, _ := oj.Params["language"].(string)
			ojForced, _ := oj.Params["forced"].(bool)
			if NormLanguage(ojLang) == lang && ojForced == forced {
				_, _ = s.Jobs.DropQueued(ctx, oj.ID)
			}
		case KindSearch, KindTranscribe:
			if !hasOpen {
				_, _ = s.Jobs.DropQueued(ctx, oj.ID)
			}
		}
	}
}

func (a *Handlers) waitingSubtitles(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := waitingLimit(r, 50, 200)
	offset, _ := strconv.Atoi(q.Get("offset"))
	if offset < 0 {
		offset = 0
	}
	list, total, err := a.svc.ListWaitingJobs(r.Context(), q.Get("q"), q.Get("kind"), limit, offset)
	if err != nil {
		webserver.Internal(w, "api", err)
		return
	}
	webserver.WriteJSON(w, http.StatusOK, map[string]any{
		"jobs":   list,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

func (a *Handlers) bumpSubtitleJob(w http.ResponseWriter, r *http.Request) {
	id, ok := subID(r)
	if !ok {
		webserver.Fail(w, http.StatusNotFound, "no such job")
		return
	}
	fresh, err := a.svc.BumpJob(r.Context(), id)
	if err != nil {
		switch {
		case errors.Is(err, database.ErrNotFound):
			webserver.Fail(w, http.StatusNotFound, "no such job")
		case errors.Is(err, jobs.ErrNotQueued):
			webserver.Fail(w, http.StatusConflict, err.Error())
		default:
			webserver.Internal(w, "api", err)
		}
		return
	}
	webserver.WriteJSON(w, http.StatusOK, map[string]any{
		"id":       fresh.ID,
		"priority": fresh.Priority,
		"state":    fresh.State,
	})
}

func (a *Handlers) skipSubtitleJob(w http.ResponseWriter, r *http.Request) {
	id, ok := subID(r)
	if !ok {
		webserver.Fail(w, http.StatusNotFound, "no such job")
		return
	}
	fileID, err := a.svc.SkipJob(r.Context(), id, actorOf(r))
	if err != nil {
		switch {
		case errors.Is(err, database.ErrNotFound):
			webserver.Fail(w, http.StatusNotFound, "no such job")
		case errors.Is(err, jobs.ErrNotQueued):
			webserver.Fail(w, http.StatusConflict, err.Error())
		default:
			webserver.Internal(w, "api", err)
		}
		return
	}
	webserver.WriteJSON(w, http.StatusOK, map[string]any{
		"id":      id,
		"skipped": true,
		"file_id": fileID,
	})
}

func (a *Handlers) skipFileSubtitles(w http.ResponseWriter, r *http.Request) {
	id, ok := subID(r)
	if !ok {
		webserver.Fail(w, http.StatusNotFound, "no such File")
		return
	}
	var in struct {
		Language string `json:"language"`
		Forced   bool   `json:"forced"`
	}
	if !decodeOptionalJSON(w, r, &in) {
		return
	}
	lang := ""
	if in.Language != "" {
		if lang = NormLanguage(in.Language); lang == "" {
			webserver.Fail(w, http.StatusBadRequest, "unknown language")
			return
		}
	}
	count, err := a.svc.SkipFile(r.Context(), id, lang, in.Forced, actorOf(r))
	if err != nil {
		subtitleError(w, err)
		return
	}
	webserver.WriteJSON(w, http.StatusOK, map[string]any{
		"file_id": id,
		"skipped": true,
		"count":   count,
	})
}

func waitingLimit(r *http.Request, def, maxVal int) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n <= 0 {
		return def
	}
	if maxVal > 0 && n > maxVal {
		return maxVal
	}
	return n
}

func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if r.Body == nil {
		return true
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		webserver.Fail(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return true
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		webserver.Fail(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}
