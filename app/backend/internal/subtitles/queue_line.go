package subtitles

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/jobs"
	"github.com/HolyArrs/holyarrs/app/backend/internal/mediafiles"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/container"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate/builtin"
)

func jobFileID(j jobs.Job) int64 {
	if j.Subject != nil && j.Subject.Type == "file" && j.Subject.ID > 0 {
		return j.Subject.ID
	}
	return 0
}

// protectedFrontOfLine returns the set of file IDs that are either currently
// running speech/translation work or within the next 2x working-queue front of
// line in each queue. Website search retries skip these files so external
// requests focus on the media further back.
func protectedFrontOfLine(runningSp, runningTr, queuedSp, queuedTr []jobs.Job, speechWorkers, translateWorkers int) map[int64]bool {
	protected := map[int64]bool{}
	runSpIDs := map[int64]bool{}
	for _, j := range runningSp {
		if id := jobFileID(j); id > 0 {
			runSpIDs[id] = true
			protected[id] = true
		}
	}
	runTrIDs := map[int64]bool{}
	for _, j := range runningTr {
		if id := jobFileID(j); id > 0 {
			runTrIDs[id] = true
			protected[id] = true
		}
	}
	wSpeech := max(len(runningSp), speechWorkers)
	if wSpeech == 0 && len(queuedSp) > 0 {
		wSpeech = 1
	}
	wTranslate := max(len(runningTr), translateWorkers)
	if wTranslate == 0 && len(queuedTr) > 0 {
		wTranslate = 1
	}
	takeDistinctQueued(queuedSp, runSpIDs, 2*wSpeech, protected)
	takeDistinctQueued(queuedTr, runTrIDs, 2*wTranslate, protected)
	return protected
}

func takeDistinctQueued(list []jobs.Job, running map[int64]bool, limit int, out map[int64]bool) {
	if limit <= 0 {
		return
	}
	seen := make(map[int64]bool, limit)
	count := 0
	for _, j := range list {
		if count >= limit {
			break
		}
		id := jobFileID(j)
		if id <= 0 || running[id] || seen[id] {
			continue
		}
		seen[id] = true
		out[id] = true
		count++
	}
}

func loadQueuedForLine(ctx context.Context, db *sql.DB, kind string, running []jobs.Job, targetDistinct int) []jobs.Job {
	const pageSize = 200
	runIDs := map[int64]bool{}
	for _, j := range running {
		if id := jobFileID(j); id > 0 {
			runIDs[id] = true
		}
	}
	distinct := map[int64]bool{}
	var out []jobs.Job
	for offset := 0; ; offset += pageSize {
		page, total, err := jobs.ListQueued(ctx, db, []string{kind}, pageSize, offset)
		if err != nil || len(page) == 0 {
			break
		}
		out = append(out, page...)
		for _, j := range page {
			if id := jobFileID(j); id > 0 && !runIDs[id] {
				distinct[id] = true
			}
		}
		if len(distinct) >= targetDistinct || offset+len(page) >= total {
			break
		}
	}
	return out
}

func (s *Service) protectedWorkFileIDs(ctx context.Context) map[int64]bool {
	if s == nil || s.Store == nil {
		return map[int64]bool{}
	}
	db := s.Store.Read()
	runningSp, _ := jobs.ListRunning(ctx, db, []string{KindTranscribe}, 1000)
	runningTr, _ := jobs.ListRunning(ctx, db, []string{KindTranslate}, 1000)
	speechReady := s.Transcriber(ctx).Ready
	translateReady := s.Translator(ctx).Ready()
	var devs []builtin.Device
	if s.Runners != nil && (speechReady || translateReady) {
		devs = s.Devices(ctx)
	}
	speechWorkers := 0
	if speechReady {
		if s.Runners != nil {
			speechWorkers = speechWorkersFromDevices(ctx, db, devs)
		} else {
			speechWorkers = SpeechWorkers(ctx, s.Store)
		}
	}
	translateWorkers := 0
	if translateReady {
		if s.Runners != nil {
			translateWorkers = translateWorkersFromDevices(devs)
		} else {
			translateWorkers = TranslatePoolWorkers(ctx, s.Store)
		}
	}
	targetSp := 2 * max(max(len(runningSp), speechWorkers), 1)
	targetTr := 2 * max(max(len(runningTr), translateWorkers), 1)
	queuedSp := loadQueuedForLine(ctx, db, KindTranscribe, runningSp, targetSp)
	queuedTr := loadQueuedForLine(ctx, db, KindTranslate, runningTr, targetTr)
	return protectedFrontOfLine(runningSp, runningTr, queuedSp, queuedTr, speechWorkers, translateWorkers)
}

func (s *Service) isProtectedFromSiteRetry(ctx context.Context, fileID int64) bool {
	if fileID <= 0 {
		return false
	}
	return s.protectedWorkFileIDs(ctx)[fileID]
}

// planLocalTranslation selects an existing readable text subtitle beside the
// video or inside it (when builtInAuto is enabled) to translate w from.
func planLocalTranslation(haves []Have, stops map[string]bool, w Want, builtInAuto bool) *TranslateRequest {
	from := w.TranslateFrom
	if from == "" {
		return nil
	}
	req := &TranslateRequest{Language: w.Language, Forced: w.Forced, From: from}
	if from == FromAny {
		req.From = ""
	}
	ordered := sourceOrder(haves, w)
	for _, h := range ordered {
		if h.Place == "beside" && fromFits(from, h.Language, w.Language) && sourceFits(w, h.Forced, h.HearingImpaired) && textFormat(h.Path) != "" && !stops[h.Path] {
			req.Source, req.Path = SourceBeside, h.Path
			return req
		}
	}
	if !builtInAuto {
		return nil
	}
	for _, h := range ordered {
		if h.Place != "built_in" || !fromFits(from, h.Language, w.Language) || !sourceFits(w, h.Forced, h.HearingImpaired) {
			continue
		}
		if h.Picture || !container.TextCodec(h.Codec) {
			continue
		}
		req.Source, req.Track = SourceBuiltIn, h.Track
		return req
	}
	return nil
}

func havesAndStopsFromRows(subs []Subtitle) ([]Have, map[string]bool) {
	haves := make([]Have, 0, len(subs))
	stops := map[string]bool{}
	for _, sub := range subs {
		h := Have{
			Language:        sub.Language,
			Forced:          sub.Forced,
			HearingImpaired: sub.HearingImpaired,
			Place:           sub.Place,
		}
		switch sub.Place {
		case "beside":
			if sub.RelativePath != nil {
				h.Path = *sub.RelativePath
				if sub.Made != nil && !sub.Transcribed {
					stops[*sub.RelativePath] = true
				}
			}
		case "built_in":
			if sub.Codec != nil {
				h.Codec = *sub.Codec
				h.Picture = container.PictureCodec(*sub.Codec)
			}
			if sub.TrackIndex != nil {
				h.Track = *sub.TrackIndex
			}
		}
		haves = append(haves, h)
	}
	return haves, stops
}

func activeSpeechFiles(ctx context.Context, db *sql.DB) map[int64]bool {
	out := map[int64]bool{}
	if running, err := jobs.ListRunning(ctx, db, []string{KindTranscribe}, 1000); err == nil {
		for _, j := range running {
			if id := jobFileID(j); id > 0 {
				out[id] = true
			}
		}
	}
	const pageSize = 500
	for offset := 0; ; offset += pageSize {
		page, total, err := jobs.ListQueued(ctx, db, []string{KindTranscribe}, pageSize, offset)
		if err != nil || len(page) == 0 {
			break
		}
		for _, j := range page {
			if id := jobFileID(j); id > 0 {
				out[id] = true
			}
		}
		if offset+len(page) >= total {
			break
		}
	}
	return out
}

func addTranslateJobKey(out map[string]bool, j jobs.Job) {
	if j.DedupeKey != nil && *j.DedupeKey != "" {
		out[*j.DedupeKey] = true
	}
	if fid := jobFileID(j); fid > 0 {
		langRaw, _ := j.Params["language"].(string)
		if lang := NormLanguage(langRaw); lang != "" {
			forced, _ := j.Params["forced"].(bool)
			out[translateKey(fid, TranslateRequest{Language: lang, Forced: forced})] = true
		}
	}
}

func activeTranslateKeys(ctx context.Context, db *sql.DB) map[string]bool {
	out := map[string]bool{}
	if running, err := jobs.ListRunning(ctx, db, []string{KindTranslate}, 1000); err == nil {
		for _, j := range running {
			addTranslateJobKey(out, j)
		}
	}
	const pageSize = 500
	for offset := 0; ; offset += pageSize {
		page, total, err := jobs.ListQueued(ctx, db, []string{KindTranslate}, pageSize, offset)
		if err != nil || len(page) == 0 {
			break
		}
		for _, j := range page {
			addTranslateJobKey(out, j)
		}
		if offset+len(page) >= total {
			break
		}
	}
	return out
}

// queueAllPendingWork enqueues every eligible speech-to-text and local
// translation job across present main video files.
func (s *Service) queueAllPendingWork(ctx context.Context) (spQueued, trQueued int) {
	if s == nil || s.Enqueue == nil || s.Store == nil {
		return 0, 0
	}
	db := s.Store.Read()
	set := ReadSettings(ctx, db)
	transcriber := s.Transcriber(ctx)
	tr := s.Translator(ctx)
	wantSpeech := transcriber.Ready && set.TranscribeMissing
	wantTranslate := tr.Ready()
	if !wantSpeech && !wantTranslate {
		return 0, 0
	}
	presentList, err := mediafiles.ListPresentMainVideoIDs(ctx, db)
	if err != nil || len(presentList) == 0 {
		return 0, 0
	}
	present := make(map[int64]bool, len(presentList))
	for _, id := range presentList {
		present[id] = true
	}

	if wantSpeech {
		activeSp := activeSpeechFiles(ctx, db)
		if fids, err := listPendingSpeechFileIDs(ctx, db); err == nil {
			for _, fid := range fids {
				if !present[fid] || activeSp[fid] {
					continue
				}
				if s.QueueTranscribe(ctx, fid, false) == nil {
					activeSp[fid] = true
					spQueued++
				}
			}
		}
	}

	if wantTranslate {
		activeTr := activeTranslateKeys(ctx, db)
		if wishes, err := listPendingTranslateWishes(ctx, db); err == nil {
			byFile := map[int64][]pendingTranslateWish{}
			var fileOrder []int64
			for _, w := range wishes {
				key := translateKey(w.FileID, TranslateRequest{Language: w.Language, Forced: w.Forced})
				if !present[w.FileID] || activeTr[key] {
					continue
				}
				if _, seen := byFile[w.FileID]; !seen {
					fileOrder = append(fileOrder, w.FileID)
				}
				byFile[w.FileID] = append(byFile[w.FileID], w)
			}
			for _, fid := range fileOrder {
				trQueued += s.queueFileTranslations(ctx, fid, byFile[fid], tr, activeTr)
			}
		}
	}

	return spQueued, trQueued
}

func (s *Service) queueFileTranslations(ctx context.Context, fid int64, wishes []pendingTranslateWish, tr Translator, activeTr map[string]bool) int {
	fc, err := s.load(ctx, fid)
	if err != nil {
		return 0
	}
	db := s.Store.Read()
	resolved, err := WishFor(ctx, db, fc.title.ID, fc.version.ID, fc.item.ID)
	if err != nil {
		return 0
	}
	subs, err := listPresentSubtitlesForFile(ctx, db, fid)
	if err != nil {
		return 0
	}
	haves, stops := havesAndStopsFromRows(subs)
	queued := 0
	for _, pw := range wishes {
		var want *Want
		for i := range resolved.Wants {
			rw := &resolved.Wants[i]
			if rw.Language == pw.Language && rw.Forced == pw.Forced {
				want = rw
				break
			}
		}
		if want == nil || want.TranslateFrom == "" {
			continue
		}
		key := translateKey(fid, TranslateRequest{Language: want.Language, Forced: want.Forced})
		if activeTr[key] {
			continue
		}
		req := planLocalTranslation(haves, stops, *want, tr.BuiltInAuto)
		if req == nil {
			continue
		}
		if s.QueueTranslate(ctx, fid, *req) == nil {
			activeTr[key] = true
			queued++
		}
	}
	return queued
}

// queueLocalTranslationsForFile queues any unlocked local translations right
// after a transcript is saved for fileID.
func (s *Service) queueLocalTranslationsForFile(ctx context.Context, fileID int64) {
	if s == nil || s.Enqueue == nil || s.Store == nil || fileID <= 0 {
		return
	}
	tr := s.Translator(ctx)
	if !tr.Ready() {
		return
	}
	db := s.Store.Read()
	wishes, err := listPendingTranslateWishesForFile(ctx, db, fileID)
	if err != nil || len(wishes) == 0 {
		return
	}
	activeTr := map[string]bool{}
	for _, w := range wishes {
		key := translateKey(fileID, TranslateRequest{Language: w.Language, Forced: w.Forced})
		if jid, ok := jobs.JobByDedupeKey(ctx, db, key); ok {
			var j jobs.Job
			if database.Get(ctx, db, "jobs", jid, &j) == nil && (j.State == "queued" || j.State == "running") {
				activeTr[key] = true
			}
		}
	}
	s.queueFileTranslations(ctx, fileID, wishes, tr, activeTr)
}

// dropSatisfiedQueuedWork removes queued translation or transcription jobs
// made redundant when a subtitle is saved from a website.
func (s *Service) dropSatisfiedQueuedWork(ctx context.Context, fileID int64, want Want) {
	if s == nil || s.Jobs == nil || s.Store == nil || fileID <= 0 {
		return
	}
	db := s.Store.Read()
	for _, key := range []string{
		translateKey(fileID, TranslateRequest{Language: want.Language, Forced: want.Forced}),
		fmt.Sprintf("%s:%d", KindTranscribe, fileID),
	} {
		if jid, ok := jobs.JobByDedupeKey(ctx, db, key); ok {
			var j jobs.Job
			if database.Get(ctx, db, "jobs", jid, &j) == nil && j.State == "queued" {
				_, _ = s.Jobs.DropQueued(ctx, jid)
			}
		}
	}
}
