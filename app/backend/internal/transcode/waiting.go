package transcode

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/jobs"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/mediafiles"
)

// WaitingJob describes one queued transcode job with its execution position.
type WaitingJob struct {
	ID           int64     `json:"id"`
	Kind         string    `json:"kind"`
	State        string    `json:"state"`
	Priority     string    `json:"priority"`
	Position     int       `json:"position"`
	FileID       int64     `json:"file_id,omitempty"`
	TitleID      int64     `json:"title_id,omitempty"`
	Title        string    `json:"title,omitempty"`
	MediaKind    string    `json:"media_kind,omitempty"`
	Season       *int      `json:"season,omitempty"`
	Episode      *int      `json:"episode,omitempty"`
	RelativePath string    `json:"relative_path,omitempty"`
	ProfileName  string    `json:"profile_name,omitempty"`
	SourceCodec  string    `json:"source_codec,omitempty"`
	SourceWidth  int       `json:"source_width,omitempty"`
	SourceHeight int       `json:"source_height,omitempty"`
	InputBytes   int64     `json:"input_bytes"`
	CreatedAt    time.Time `json:"created_at"`
	RunAfter     time.Time `json:"run_after"`
}

// ListWaitingJobs returns queued transcode jobs in execution order, filtered and paginated.
func (s *Service) ListWaitingJobs(ctx context.Context, query string, limit, offset int) ([]WaitingJob, int, error) {
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

	var list []jobs.Job
	var total int
	var err error
	if qLower == "" {
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

	fileIDs := make([]int64, 0, len(list))
	for _, j := range list {
		if fid, ok := fileIDParam(j); ok {
			fileIDs = append(fileIDs, fid)
		}
	}
	summaries, err := mediafiles.GetTranscodeFileSummariesByIDs(ctx, db, fileIDs)
	if err != nil {
		return nil, 0, err
	}
	itemsMap, err := listItemsByFileIDs(ctx, db, fileIDs)
	if err != nil {
		return nil, 0, err
	}
	defProfName, folderProfiles := s.folderProfileNameMap(ctx)

	toWaiting := func(idx int, j jobs.Job) WaitingJob {
		pos := idx + 1
		if qLower == "" {
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
		if fid, ok := fileIDParam(j); ok {
			wj.FileID = fid
			if meta, exists := summaries[fid]; exists {
				wj.TitleID = meta.TitleID
				wj.Title = meta.Title
				wj.MediaKind = meta.MediaKind
				wj.Season = meta.Season
				wj.Episode = meta.Episode
				wj.RelativePath = meta.RelativePath
				wj.SourceCodec = meta.SourceCodec
				wj.InputBytes = meta.Size
				if pName := folderProfiles[meta.LibraryFolderID]; pName != "" {
					wj.ProfileName = pName
				} else {
					wj.ProfileName = defProfName
				}
			}
			if rec, exists := itemsMap[fid]; exists {
				if rec.SourceCodec != "" {
					wj.SourceCodec = rec.SourceCodec
				}
				wj.SourceWidth = rec.SourceWidth
				wj.SourceHeight = rec.SourceHeight
				if rec.InputBytes > 0 {
					wj.InputBytes = rec.InputBytes
				}
			}
		}
		return wj
	}

	if qLower == "" {
		out := make([]WaitingJob, 0, len(list))
		for idx, j := range list {
			out = append(out, toWaiting(idx, j))
		}
		return out, total, nil
	}

	matched := make([]WaitingJob, 0, len(list))
	for idx, j := range list {
		wj := toWaiting(idx, j)
		if !strings.Contains(strings.ToLower(wj.Title), qLower) &&
			!strings.Contains(strings.ToLower(wj.RelativePath), qLower) &&
			!strings.Contains(strings.ToLower(wj.ProfileName), qLower) &&
			!strings.Contains(strings.ToLower(wj.Kind), qLower) &&
			!strings.Contains(strings.ToLower(wj.SourceCodec), qLower) {
			continue
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

// BumpJob moves a queued transcode job to the front of the waiting queue.
func (s *Service) BumpJob(ctx context.Context, id int64) (jobs.Job, error) {
	if s.Jobs == nil {
		return jobs.Job{}, errors.New("no job queue")
	}
	var j jobs.Job
	if err := database.Get(ctx, s.Store.Read(), "jobs", id, &j); err != nil {
		return jobs.Job{}, err
	}
	if !isTranscodeJobKind(j.Kind) {
		return jobs.Job{}, database.ErrNotFound
	}
	return s.Jobs.Promote(ctx, id)
}

// SkipJob drops a queued transcode job cleanly and marks its file user_skipped.
func (s *Service) SkipJob(ctx context.Context, id int64, by domain.Actor) (int64, error) {
	if s.Jobs == nil {
		return 0, errors.New("no job queue")
	}
	var j jobs.Job
	if err := database.Get(ctx, s.Store.Read(), "jobs", id, &j); err != nil {
		return 0, err
	}
	if !isTranscodeJobKind(j.Kind) {
		return 0, database.ErrNotFound
	}
	if j.State != "queued" {
		return 0, fmt.Errorf("%w: %s", jobs.ErrNotQueued, j.State)
	}
	dropped, err := s.Jobs.DropQueued(ctx, id)
	if err != nil {
		return 0, err
	}
	fileID, ok := fileIDParam(dropped)
	if ok {
		if err := s.markUserSkipped(ctx, fileID, nil, by); err != nil {
			return fileID, err
		}
	}
	return fileID, nil
}

// SkipFile drops any queued transcode job for fileID and marks the file user_skipped.
func (s *Service) SkipFile(ctx context.Context, fileID int64, by domain.Actor) error {
	fc, err := mediafiles.LoadTranscodeFile(ctx, s.Store.Read(), fileID)
	if err != nil {
		return err
	}
	if s.Jobs != nil {
		dedupeKey := fmt.Sprintf("transcode:%d", fileID)
		if jid, ok := jobs.JobByDedupeKey(ctx, s.Store.Read(), dedupeKey); ok {
			var j jobs.Job
			if database.Get(ctx, s.Store.Read(), "jobs", jid, &j) == nil && j.State == "queued" {
				_, _ = s.Jobs.DropQueued(ctx, jid)
			}
		}
	}
	return s.markUserSkipped(ctx, fileID, &fc, by)
}

func (s *Service) markUserSkipped(ctx context.Context, fileID int64, fcOpt *mediafiles.TranscodeFileContext, by domain.Actor) error {
	rec, found, err := getItemByFileID(ctx, s.Store.Read(), fileID)
	if err != nil {
		return err
	}
	if !found {
		rec = ItemRecord{FileID: fileID}
	}
	if fcOpt == nil {
		if fc, fErr := mediafiles.LoadTranscodeFile(ctx, s.Store.Read(), fileID); fErr == nil {
			fcOpt = &fc
		}
	}

	now := time.Now().UTC()
	skip := "user_skipped"
	rec.State = "skipped"
	rec.SkipReason = &skip
	rec.LastError = nil
	if rec.ProbedAt == nil {
		rec.ProbedAt = &now
	}

	if fcOpt != nil {
		if _, prof, pErr := s.ResolveFolderProfile(ctx, fcOpt.Version.LibraryFolderID); pErr == nil {
			pid := prof.ID
			rec.ProfileID = &pid
			rec.TargetCodec = prof.TargetCodec
		}
		if rec.SourceCodec == "" {
			rec.SourceCodec = strings.ToLower(fcOpt.File.Quality.Codec)
		}
		fullPath := filepath.Join(fcOpt.LibPath, filepath.FromSlash(fcOpt.File.RelativePath))
		if info, statErr := os.Stat(fullPath); statErr == nil {
			rec.FileSizeBytes = info.Size()
			rec.FileMtimeNs = info.ModTime().UnixNano()
			rec.InputBytes = info.Size()
		} else if fcOpt.File.Size > 0 {
			rec.FileSizeBytes = fcOpt.File.Size
			if rec.InputBytes == 0 {
				rec.InputBytes = fcOpt.File.Size
			}
		}
	}
	return upsertItem(ctx, s.Store, &rec, by)
}
