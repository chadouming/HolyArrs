package subtitles

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/jobs"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/importer"
	"github.com/HolyArrs/holyarrs/app/backend/internal/mediafiles"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/subtext"
)

// KindVerify is the Job that checks beside subtitles in the library for
// corruption, truncation, or mismatched duration.
const KindVerify = "subtitle_verify"

// IntegritySummary counts beside subtitles by integrity check outcome.
type IntegritySummary struct {
	OK            int        `json:"ok"`
	Bad           int        `json:"bad"`
	Accepted      int        `json:"accepted"`
	Unchecked     int        `json:"unchecked"`
	LastCheckedAt *time.Time `json:"last_checked_at,omitempty"`
}

// IntegrityRow is one beside subtitle flagged with an integrity issue.
type IntegrityRow struct {
	ID              int64      `json:"id"`
	FileID          int64      `json:"file_id"`
	TitleID         int64      `json:"title_id"`
	Title           string     `json:"title"`
	Kind            string     `json:"kind"`
	Season          *int       `json:"season,omitempty"`
	Episode         *int       `json:"episode,omitempty"`
	Language        string     `json:"language"`
	Forced          bool       `json:"forced"`
	HearingImpaired bool       `json:"hearing_impaired"`
	RelativePath    string     `json:"relative_path"`
	Issue           string     `json:"issue"`
	Note            string     `json:"note"`
	Coverage        *float64   `json:"coverage,omitempty"`
	LastMS          *int64     `json:"last_ms,omitempty"`
	VideoSeconds    *float64   `json:"video_seconds,omitempty"`
	CheckedAt       *time.Time `json:"checked_at,omitempty"`
}

// QueueVerify queues a full library subtitle integrity verification job.
func (s *Service) QueueVerify(ctx context.Context) error {
	if s.Enqueue == nil {
		return errors.New("no job queue")
	}
	return s.Enqueue(ctx, KindVerify, nil, nil, KindVerify)
}

func (s *Service) handleVerify(ctx context.Context, r *jobs.Run) error {
	return s.VerifyAll(ctx, r)
}

const verifyBatchSize = 50

// VerifyAll inspects every present beside subtitle (except accepted ones) and
// stores its integrity outcome.
func (s *Service) VerifyAll(ctx context.Context, r *jobs.Run) error {
	rows, err := listPresentBesideForVerify(ctx, s.Store.Read())
	if err != nil {
		return err
	}
	type cachedFileCtx struct {
		fc  *fileCtx
		err error
	}
	fileCache := make(map[int64]cachedFileCtx)
	batch := make([]integrityResultUpdate, 0, verifyBatchSize)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := setIntegrityResultsBatch(ctx, s.Store, batch); err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	}

	var okCount, badCount, skipped int
	total := len(rows)
	for i, row := range rows {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		cf, ok := fileCache[row.FileID]
		if !ok {
			cf.fc, cf.err = s.load(ctx, row.FileID)
			fileCache[row.FileID] = cf
		}
		if cf.err != nil {
			skipped++
			continue
		}
		rep := verifyRow(cf.fc, row)
		state := "ok"
		if !rep.OK {
			state = "bad"
			badCount++
		} else {
			okCount++
		}
		batch = append(batch, integrityResultUpdate{
			id:       row.ID,
			state:    state,
			issue:    string(rep.Issue),
			note:     rep.Reason,
			coverage: rep.Coverage,
			lastMS:   rep.LastMS,
			videoSec: rep.VideoSeconds,
			at:       now(),
		})
		if len(batch) >= verifyBatchSize {
			if err := flush(); err != nil {
				return err
			}
		}
		if r != nil && r.Job.ID != 0 && ((i+1)%10 == 0 || i+1 == total) {
			_ = r.Progress(ctx, float64(i+1)/float64(total), "verifying")
		}
	}
	if err := flush(); err != nil {
		return err
	}
	if r != nil {
		r.SetResult(map[string]any{"checked": okCount + badCount, "ok": okCount, "bad": badCount, "skipped": skipped})
	}
	return nil
}

func verifyRow(fc *fileCtx, row Subtitle) subtext.IntegrityReport {
	videoSec := fc.videoSeconds()
	if row.RelativePath == nil || *row.RelativePath == "" {
		return subtext.IntegrityReport{Issue: subtext.IssueUnreadable, Reason: "Subtitle has no file path", VideoSeconds: videoSec}
	}
	rel := *row.RelativePath
	full := filepath.Join(fc.lib, filepath.FromSlash(rel))
	if !strings.HasPrefix(full, filepath.Clean(fc.lib)+string(filepath.Separator)) || !importer.InsideReal(fc.lib, full) {
		return subtext.IntegrityReport{Issue: subtext.IssueUnreadable, Reason: "Subtitle path is outside the Library folder", VideoSeconds: videoSec}
	}
	fi, err := os.Lstat(full)
	if err != nil {
		return subtext.IntegrityReport{Issue: subtext.IssueUnreadable, Reason: "Subtitle file cannot be read", VideoSeconds: videoSec}
	}
	if !fi.Mode().IsRegular() || fi.Size() > subtext.MaxText {
		return subtext.IntegrityReport{Issue: subtext.IssueUnreadable, Reason: "Subtitle is not a regular file under 5 MB", VideoSeconds: videoSec}
	}
	b, err := os.ReadFile(full)
	if err != nil {
		return subtext.IntegrityReport{Issue: subtext.IssueUnreadable, Reason: "Subtitle file cannot be read", VideoSeconds: videoSec}
	}
	format := strings.TrimPrefix(strings.ToLower(path.Ext(rel)), ".")
	if format == subtext.SUB {
		if len(bytes.TrimSpace(b)) == 0 {
			return subtext.IntegrityReport{Issue: subtext.IssueEmpty, Reason: "Subtitle file is empty", VideoSeconds: videoSec}
		}
		return subtext.IntegrityReport{OK: true, VideoSeconds: videoSec}
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return subtext.IntegrityReport{Issue: subtext.IssueEmpty, Reason: "Subtitle file is empty", VideoSeconds: videoSec}
	}
	text, err := subtext.Clean(b, format)
	if err != nil {
		if errors.Is(err, subtext.ErrNotSubtitle) {
			return subtext.IntegrityReport{Issue: subtext.IssueUnreadable, Reason: "Subtitle contains no valid cues", VideoSeconds: videoSec}
		}
		return subtext.IntegrityReport{Issue: subtext.IssueUnreadable, Reason: err.Error(), VideoSeconds: videoSec}
	}
	return subtext.CheckIntegrity(text, format, videoSec, row.Forced)
}

// IntegritySummary returns counts of present beside subtitles by integrity state.
func (s *Service) IntegritySummary(ctx context.Context) (IntegritySummary, error) {
	db := s.Store.Read()
	buckets, err := integritySummaryStore(ctx, db)
	if err != nil {
		return IntegritySummary{}, err
	}
	var out IntegritySummary
	if len(buckets) == 0 {
		return out, nil
	}
	presentIDs, err := mediafiles.ListPresentMainVideoIDs(ctx, db)
	if err != nil {
		return out, err
	}
	present := make(map[int64]bool, len(presentIDs))
	for _, id := range presentIDs {
		present[id] = true
	}
	for _, b := range buckets {
		if !present[b.fileID] {
			continue
		}
		switch b.state {
		case "ok":
			out.OK += b.count
		case "bad":
			out.Bad += b.count
		case "accepted":
			out.Accepted += b.count
		default:
			out.Unchecked += b.count
		}
		if b.maxAt != "" {
			if t, err := time.Parse(time.RFC3339Nano, b.maxAt); err == nil {
				if out.LastCheckedAt == nil || t.After(*out.LastCheckedAt) {
					out.LastCheckedAt = &t
				}
			}
		}
	}
	return out, nil
}

// IntegrityIssues returns a paginated list of active beside subtitles flagged as bad.
func (s *Service) IntegrityIssues(ctx context.Context, limit, offset int) ([]IntegrityRow, int, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	db := s.Store.Read()
	rows, err := listBadBeside(ctx, db)
	if err != nil {
		return nil, 0, err
	}
	if len(rows) == 0 {
		return []IntegrityRow{}, 0, nil
	}
	fileIDs := make([]int64, 0, len(rows))
	for _, r := range rows {
		fileIDs = append(fileIDs, r.FileID)
	}
	summaries, err := mediafiles.GetPresentMainFileSummariesByIDs(ctx, db, fileIDs)
	if err != nil {
		return nil, 0, err
	}
	active := make([]IntegrityRow, 0, len(rows))
	for _, r := range rows {
		meta, ok := summaries[r.FileID]
		if !ok {
			continue
		}
		ir := IntegrityRow{
			ID:              r.ID,
			FileID:          r.FileID,
			TitleID:         meta.TitleID,
			Title:           meta.Title,
			Kind:            meta.MediaKind,
			Season:          meta.Season,
			Episode:         meta.Episode,
			Language:        r.Language,
			Forced:          r.Forced,
			HearingImpaired: r.HearingImpaired,
			Coverage:        r.IntegrityCoverage,
			LastMS:          r.IntegrityLastMS,
			VideoSeconds:    r.IntegrityVideoS,
			CheckedAt:       r.IntegrityCheckedAt,
		}
		if r.RelativePath != nil {
			ir.RelativePath = *r.RelativePath
		}
		if r.IntegrityIssue != nil {
			ir.Issue = *r.IntegrityIssue
		}
		if r.IntegrityNote != nil {
			ir.Note = *r.IntegrityNote
		}
		active = append(active, ir)
	}
	total := len(active)
	if offset >= total {
		return []IntegrityRow{}, total, nil
	}
	end := min(offset+limit, total)
	return active[offset:end], total, nil
}

// ReplaceCorrupt moves a bad beside subtitle to the holding folder, marks it
// replaced, turns down its site candidate if fetched by a site, re-scans the
// file's wishes, and queues a fresh subtitle search.
func (s *Service) ReplaceCorrupt(ctx context.Context, id int64, by domain.Actor) error {
	row, fc, err := s.besideRow(ctx, id)
	if err != nil {
		return err
	}
	rel := *row.RelativePath
	full := filepath.Join(fc.lib, filepath.FromSlash(rel))
	if !strings.HasPrefix(full, filepath.Clean(fc.lib)+string(filepath.Separator)) || !importer.InsideReal(fc.lib, full) {
		return fmt.Errorf("%s is outside the Library folder", rel)
	}
	to, err := importer.HoldPath(fc.lib, rel, now())
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o777); err != nil {
		return err
	}
	if err := os.Rename(full, to); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	reason := "failed subtitle integrity check"
	if row.IntegrityNote != nil && *row.IntegrityNote != "" {
		reason = *row.IntegrityNote
	} else if row.IntegrityIssue != nil && *row.IntegrityIssue != "" {
		reason = *row.IntegrityIssue
	}
	if len(reason) > 500 {
		reason = reason[:500]
	}
	if err := markReplacedAndTurnDown(ctx, s.Store, row, reason, by, now()); err != nil {
		return err
	}
	if _, err := s.look(ctx, fc, by); err != nil {
		return err
	}
	if s.Enqueue != nil {
		return s.Queue(ctx, row.FileID, nil)
	}
	return nil
}

// KeepIntegrity marks a flagged beside subtitle as accepted so future checks skip it.
func (s *Service) KeepIntegrity(ctx context.Context, id int64, by domain.Actor) error {
	if _, _, err := s.besideRow(ctx, id); err != nil {
		return err
	}
	return acceptIntegrity(ctx, s.Store, id, by, now())
}
