package transcode

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/jobs"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/mediafiles"
)

// Job kinds handled by the transcode module.
const (
	KindFile = "transcode_file"
	KindScan = "transcode_scan"
)

const overviewCount = 500

var overviewKinds = []string{KindFile, KindScan}

// ErrFolderDisabled is returned when manually queueing an item in a disabled library folder without force.
var ErrFolderDisabled = errors.New("transcoding is disabled for this library folder")

// Publisher sends live progress events. live.Hub satisfies it.
type Publisher interface {
	Publish(topic string, data any) uint64
}

type liveStat struct {
	phase       string
	encoder     string
	gpuDevice   string
	selectedQ   *int
	meanVMAF    *float64
	fps         float64
	speed       float64
	profileName string
	inputBytes  int64
	updatedAt   time.Time
}

// Service coordinates hardware probing, queue scheduling, and transcode execution.
type Service struct {
	Store        *database.Store
	Live         Publisher
	Jobs         *jobs.Queue
	Enqueue      func(ctx context.Context, kind string, subj *domain.Subject, params map[string]any, key string) error
	OnTranscoded func(ctx context.Context, fileID int64)
	YieldGPU     func(ctx context.Context)
	GPUVRAMProbe func(ctx context.Context) []GPUVRAMInfo
	YieldGPUVRAM func(ctx context.Context, card int, reserveMB int)
	DevDRI       string
	SysfsDRM     string

	gpus          gpuAllocator
	paused        atomic.Bool
	concMu        sync.Mutex
	concCond      *sync.Cond
	concGen       uint64
	activeEncodes int
	statsMu       sync.Mutex
	liveStats     map[int64]liveStat
	vramMu         sync.Mutex
	vramCache      []GPUVRAMInfo
	vramCacheAt    time.Time
	vramRefreshing bool
	vramWait       chan struct{}
	activeVRAM     map[int]int
	hwMu          sync.Mutex
	hwCache       *cachedHardwareProbe
}

// NewService creates a ready-to-register transcode Service.
func NewService(store *database.Store, live Publisher, q *jobs.Queue) *Service {
	s := &Service{
		Store:     store,
		Live:      live,
		Jobs:      q,
		liveStats: map[int64]liveStat{},
	}
	s.concCond = sync.NewCond(&s.concMu)
	return s
}

func (s *Service) ensureCond() {
	s.concMu.Lock()
	if s.concCond == nil {
		s.concCond = sync.NewCond(&s.concMu)
	}
	s.concMu.Unlock()
}

func (s *Service) notifyConcurrency() {
	s.ensureCond()
	s.concMu.Lock()
	s.concGen++
	s.concCond.Broadcast()
	s.concMu.Unlock()
}

func (s *Service) acquireEncodeSlot(ctx context.Context, limitFn func() int) error {
	s.ensureCond()
	stopWake := context.AfterFunc(ctx, s.notifyConcurrency)
	defer stopWake()

	for {
		s.concMu.Lock()
		if err := ctx.Err(); err != nil {
			s.concMu.Unlock()
			return err
		}
		gen := s.concGen
		s.concMu.Unlock()

		limit := 1
		if limitFn != nil {
			limit = max(1, limitFn())
		}

		s.concMu.Lock()
		if err := ctx.Err(); err != nil {
			s.concMu.Unlock()
			return err
		}
		if !s.Paused() && s.activeEncodes < limit {
			s.activeEncodes++
			s.concMu.Unlock()
			return nil
		}
		if s.concGen != gen {
			s.concMu.Unlock()
			continue
		}
		s.concCond.Wait()
		s.concMu.Unlock()
	}
}

func (s *Service) releaseEncodeSlot() {
	s.concMu.Lock()
	if s.activeEncodes > 0 {
		s.activeEncodes--
	}
	s.concGen++
	if s.concCond != nil {
		s.concCond.Broadcast()
	}
	s.concMu.Unlock()
}

type fileJobState struct {
	st          Settings
	fc          mediafiles.TranscodeFileContext
	libEnabled  bool
	prof        Profile
	inputPath   string
	origSize    int64
	origMtimeNs int64
}

func (s *Service) loadFileJobState(ctx context.Context, fileID int64) (fileJobState, bool, error) {
	st := ReadSettings(ctx, s.Store.Read())
	fc, err := mediafiles.LoadTranscodeFile(ctx, s.Store.Read(), fileID)
	if err != nil {
		if errors.Is(err, database.ErrNotFound) || errors.Is(err, mediafiles.ErrTranscodeNotMain) {
			return fileJobState{}, true, nil
		}
		return fileJobState{}, false, err
	}
	libEnabled, prof, err := s.ResolveFolderProfile(ctx, fc.Version.LibraryFolderID)
	if err != nil {
		return fileJobState{}, false, err
	}
	inputPath := filepath.Join(fc.LibPath, filepath.FromSlash(fc.File.RelativePath))
	srcInfo, err := os.Stat(inputPath)
	if err != nil {
		return fileJobState{}, false, fmt.Errorf("stat source %s: %w", inputPath, err)
	}
	return fileJobState{
		st:          st,
		fc:          fc,
		libEnabled:  libEnabled,
		prof:        prof,
		inputPath:   inputPath,
		origSize:    srcInfo.Size(),
		origMtimeNs: srcInfo.ModTime().UnixNano(),
	}, false, nil
}

func (s *Service) setLiveStat(jobID int64, st liveStat) {
	if jobID == 0 {
		return
	}
	st.updatedAt = time.Now()
	s.statsMu.Lock()
	if s.liveStats == nil {
		s.liveStats = map[int64]liveStat{}
	}
	s.liveStats[jobID] = st
	s.statsMu.Unlock()
}

func (s *Service) clearLiveStat(jobID int64) {
	s.statsMu.Lock()
	delete(s.liveStats, jobID)
	s.statsMu.Unlock()
}

func (s *Service) setActiveGPUVRAM(card int, deltaMB int) {
	s.vramMu.Lock()
	if s.activeVRAM == nil {
		s.activeVRAM = map[int]int{}
	}
	next := s.activeVRAM[card] + deltaMB
	if next <= 0 {
		delete(s.activeVRAM, card)
	} else {
		s.activeVRAM[card] = next
	}
	s.vramMu.Unlock()
}

// ActiveGPUVRAMMB returns the VRAM MiB currently reserved by active transcodes on card
// (or the max across active cards when card < 0).
func (s *Service) ActiveGPUVRAMMB(card int) int {
	s.vramMu.Lock()
	defer s.vramMu.Unlock()
	if card < 0 {
		best := 0
		for _, v := range s.activeVRAM {
			if v > best {
				best = v
			}
		}
		return best
	}
	if v := s.activeVRAM[card]; v > 0 {
		return v
	}
	return 0
}

func estimateTranscodeVRAMMB(srcHeight int, extraFrames int) int {
	if extraFrames <= 0 {
		extraFrames = 24
	}
	if srcHeight >= 2000 {
		return 450 + extraFrames*26
	}
	return 350 + extraFrames*7
}

func (s *Service) getLiveStat(jobID int64) (liveStat, bool) {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	st, ok := s.liveStats[jobID]
	return st, ok
}

// Register attaches job handlers to q and seeds the built-in Default profile.
func (s *Service) Register(q *jobs.Queue) {
	s.Jobs = q
	q.Handle(KindFile, s.handleFileJob)
	q.Handle(KindScan, s.handleScanJob)
	_ = s.EnsureDefaultProfile(context.Background())
}

func (s *Service) enqueueJob(ctx context.Context, kind string, subj *domain.Subject, params map[string]any, key string) error {
	if s.Enqueue != nil {
		return s.Enqueue(ctx, kind, subj, params, key)
	}
	if s.Jobs == nil {
		return errors.New("job queue unavailable")
	}
	_, _, err := s.Jobs.Enqueue(ctx, kind, subj, params, key, domain.System)
	return err
}

// QueueFile queues a single library file for transcoding.
func (s *Service) QueueFile(ctx context.Context, fileID int64, force bool) error {
	fc, err := mediafiles.LoadTranscodeFile(ctx, s.Store.Read(), fileID)
	if err != nil {
		return err
	}
	enabled, _, err := s.ResolveFolderProfile(ctx, fc.Version.LibraryFolderID)
	if err != nil {
		return err
	}
	if !force && !enabled {
		return ErrFolderDisabled
	}
	clearItemError(ctx, s.Store, fileID)
	subj := &database.Subject{Type: "file", ID: fileID}
	params := map[string]any{"file_id": fileID, "force": force}
	key := fmt.Sprintf("transcode:%d", fileID)
	return s.enqueueJob(ctx, KindFile, subj, params, key)
}

// QueueScan queues a library probe scan job.
func (s *Service) QueueScan(ctx context.Context, queueMissing bool) error {
	params := map[string]any{"queue_missing": queueMissing}
	return s.enqueueJob(ctx, KindScan, nil, params, "transcode:scan")
}

// QueueAll queues pending files across enabled library folders.
func (s *Service) QueueAll(ctx context.Context, limit int) (int, int, error) {
	resolver, err := s.newFolderProfileResolver(ctx)
	if err != nil {
		return 0, 0, err
	}
	db := s.Store.Read()
	files, err := mediafiles.ListPresentMainVideoScanRows(ctx, db)
	if err != nil {
		return 0, 0, err
	}
	itemsMap, err := listItemsByFileID(ctx, db)
	if err != nil {
		return 0, 0, err
	}
	queued := 0
	skipped := 0
	for _, f := range files {
		if limit > 0 && queued >= limit {
			break
		}
		enabled, _ := resolver.resolve(f.LibraryFolderID)
		if !enabled {
			skipped++
			continue
		}
		rec, hasItem := itemsMap[f.FileID]
		if hasItem && (rec.State == "done" || rec.State == "skipped") {
			skipped++
			continue
		}
		if hasItem && rec.LastError != nil && *rec.LastError != "" {
			clearItemError(ctx, s.Store, f.FileID)
		}
		subj := &database.Subject{Type: "file", ID: f.FileID}
		params := map[string]any{"file_id": f.FileID, "force": false}
		key := fmt.Sprintf("transcode:%d", f.FileID)
		if err := s.enqueueJob(ctx, KindFile, subj, params, key); err != nil {
			skipped++
			continue
		}
		queued++
	}
	return queued, skipped, nil
}

// UpdateSettings saves settings and wakes concurrency waiters.
func (s *Service) UpdateSettings(ctx context.Context, st Settings, by domain.Actor) error {
	if err := SaveSettings(ctx, s.Store, st, by); err != nil {
		return err
	}
	s.notifyConcurrency()
	return nil
}

// OnEvent queues newly imported, replaced, or adopted files when auto_transcode is enabled.
func (s *Service) OnEvent(e domain.Event) {
	ctx := context.Background()
	st := ReadSettings(ctx, s.Store.Read())
	if !st.AutoTranscode {
		return
	}
	switch e.Kind {
	case "file_replaced":
		if newID, ok := numFromAny(e.Data["new_file_id"]); ok && newID > 0 {
			if err := s.QueueFile(ctx, newID, false); err != nil {
				log.Printf("transcode: auto queue file %d: %v", newID, err)
			}
		}
	case "adopted":
		if err := s.QueueScan(ctx, true); err != nil {
			log.Printf("transcode: auto scan after adopt: %v", err)
		}
	case "import_done", "import_partly_done":
		slots := extractSlotIDs(e)
		fileIDs, err := mediafiles.MainFileIDsForSlots(ctx, s.Store.Read(), slots)
		if err != nil {
			return
		}
		for _, fid := range fileIDs {
			if err := s.QueueFile(ctx, fid, false); err != nil {
				log.Printf("transcode: auto queue file %d: %v", fid, err)
			}
		}
	}
}

func extractSlotIDs(e domain.Event) []int64 {
	var slots []int64
	for _, k := range []string{"placed_slot_ids", "slot_ids"} {
		switch v := e.Data[k].(type) {
		case []any:
			for _, x := range v {
				if id, ok := numFromAny(x); ok && id > 0 {
					slots = append(slots, id)
				}
			}
		case []int64:
			slots = append(slots, v...)
		}
	}
	if len(slots) == 0 && e.SlotID != nil && *e.SlotID > 0 {
		slots = append(slots, *e.SlotID)
	}
	return slots
}

func sameFileOnDisk(rec ItemRecord, size, mtimeNs int64) bool {
	if rec.FileSizeBytes <= 0 || rec.FileSizeBytes != size {
		return false
	}
	diff := rec.FileMtimeNs - mtimeNs
	if diff < 0 {
		diff = -diff
	}
	return diff <= int64(2*time.Second)
}

type folderProfileChoice struct {
	enabled bool
	prof    Profile
}

type folderProfileResolver struct {
	defaultProf Profile
	byFolder    map[int64]folderProfileChoice
}

func (s *Service) newFolderProfileResolver(ctx context.Context) (folderProfileResolver, error) {
	if err := s.EnsureDefaultProfile(ctx); err != nil {
		return folderProfileResolver{}, err
	}
	db := s.Store.Read()
	pRows, err := listProfileRows(ctx, db)
	if err != nil {
		return folderProfileResolver{}, err
	}
	byID := make(map[int64]Profile, len(pRows))
	var defProf Profile
	for _, r := range pRows {
		p := rowToProfile(r)
		byID[p.ID] = p
		if p.IsDefault || defProf.ID == 0 {
			defProf = p
		}
	}
	assigns, err := listLibraryProfileRows(ctx, db)
	if err != nil {
		assigns = map[int64]libraryProfileRow{}
	}
	byFolder := make(map[int64]folderProfileChoice, len(assigns))
	for fid, row := range assigns {
		p := defProf
		if row.ProfileID != nil {
			if found, ok := byID[*row.ProfileID]; ok {
				p = found
			}
		}
		byFolder[fid] = folderProfileChoice{enabled: row.Enabled, prof: p}
	}
	return folderProfileResolver{defaultProf: defProf, byFolder: byFolder}, nil
}

func (r folderProfileResolver) resolve(folderID int64) (bool, Profile) {
	if c, ok := r.byFolder[folderID]; ok {
		return c.enabled, c.prof
	}
	return true, r.defaultProf
}

type scanOutcome struct {
	rec        *ItemRecord
	queueID    int64
	needsQueue bool
}

func (s *Service) scanOneFile(ctx context.Context, f mediafiles.TranscodeFileSummary, resolver folderProfileResolver, itemsMap map[int64]ItemRecord, ffprobeBin string, queueMissing bool) scanOutcome {
	enabled, prof := resolver.resolve(f.LibraryFolderID)
	fullPath := filepath.Join(f.LibPath, filepath.FromSlash(f.RelativePath))
	info, statErr := os.Stat(fullPath)
	if statErr != nil {
		return scanOutcome{}
	}
	mtimeNs := info.ModTime().UnixNano()
	size := info.Size()

	if rec, exists := itemsMap[f.FileID]; exists {
		switch rec.State {
		case "done":
			if rec.FileSizeBytes > 0 && rec.FileSizeBytes == size &&
				(rec.TargetCodec == "" || rec.TargetCodec == prof.TargetCodec) {
				return scanOutcome{}
			}
		case "skipped":
			if rec.SkipReason != nil && *rec.SkipReason == "user_skipped" &&
				rec.FileSizeBytes > 0 && rec.FileSizeBytes == size {
				return scanOutcome{}
			}
			if rec.SkipReason != nil && *rec.SkipReason == "library_disabled" {
				if !enabled {
					return scanOutcome{}
				}
			} else if enabled && sameFileOnDisk(rec, size, mtimeNs) && rec.TargetCodec == prof.TargetCodec {
				return scanOutcome{}
			}
		case "pending":
			if enabled && sameFileOnDisk(rec, size, mtimeNs) &&
				rec.TargetCodec == prof.TargetCodec && rec.ProbedAt != nil {
				return scanOutcome{queueID: f.FileID, needsQueue: queueMissing}
			}
		}
	}

	now := time.Now().UTC()
	profID := prof.ID
	rec := ItemRecord{
		FileID:        f.FileID,
		ProfileID:     &profID,
		State:         "pending",
		FileMtimeNs:   mtimeNs,
		FileSizeBytes: size,
		TargetCodec:   prof.TargetCodec,
		InputBytes:    size,
		ProbedAt:      &now,
	}

	if !enabled {
		reason := "library_disabled"
		rec.State = "skipped"
		rec.SkipReason = &reason
		return scanOutcome{rec: &rec}
	}

	if fp, fpErr := FastProbeFile(ctx, ffprobeBin, fullPath); fpErr == nil {
		rec.FileFlowsTagged = MatchSkipTags(fp.Tags, prof.SkipTags)
		rec.SourceCodec = fp.SourceCodec
		rec.SourceWidth = fp.SourceWidth
		rec.SourceHeight = fp.SourceHeight
		rec.SourceHDR = fp.SourceHDR
		if skip, reason := EvaluateFastProbe(fp, fullPath, size, prof.ProfileConfig, enabled, false); skip {
			rec.State = "skipped"
			rec.SkipReason = &reason
		}
	} else if f.SourceCodec != "" {
		rec.SourceCodec = f.SourceCodec
	}

	return scanOutcome{
		rec:        &rec,
		queueID:    f.FileID,
		needsQueue: queueMissing && rec.State == "pending",
	}
}

func (s *Service) handleScanJob(ctx context.Context, r *jobs.Run) error {
	resolver, err := s.newFolderProfileResolver(ctx)
	if err != nil {
		return err
	}
	queueMissing, _ := r.Job.Params["queue_missing"].(bool)
	st := ReadSettings(ctx, s.Store.Read())
	hw := s.probeHardwareWithSettings(ctx, st)

	db := s.Store.Read()
	files, err := mediafiles.ListPresentMainVideoScanRows(ctx, db)
	if err != nil {
		return err
	}
	itemsMap, err := listItemsByFileID(ctx, db)
	if err != nil {
		return err
	}

	total := len(files)
	if total == 0 {
		_ = r.Progress(ctx, 1.0, "done")
		return nil
	}

	const workers = 6
	jobsCh := make(chan mediafiles.TranscodeFileSummary, workers*2)
	resultsCh := make(chan scanOutcome, workers*2)

	var wg sync.WaitGroup
	for range min(workers, total) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range jobsCh {
				if ctx.Err() != nil {
					return
				}
				resultsCh <- s.scanOneFile(ctx, f, resolver, itemsMap, hw.FFprobePath, queueMissing)
			}
		}()
	}

	go func() {
		defer close(jobsCh)
		for _, f := range files {
			select {
			case <-ctx.Done():
				return
			case jobsCh <- f:
			}
		}
	}()

	go func() {
		wg.Wait()
		close(resultsCh)
	}()

	const batchSize = 50
	batch := make([]ItemRecord, 0, batchSize)
	var toQueue []int64
	processed := 0
	lastProg := time.Time{}

	for out := range resultsCh {
		processed++
		if out.rec != nil {
			batch = append(batch, *out.rec)
			if len(batch) >= batchSize {
				_ = upsertItemsBatch(ctx, s.Store, batch, domain.System)
				batch = batch[:0]
			}
		}
		if out.needsQueue {
			toQueue = append(toQueue, out.queueID)
		}
		if processed == 1 || processed%25 == 0 || time.Since(lastProg) >= 250*time.Millisecond {
			lastProg = time.Now()
			_ = r.Progress(ctx, float64(processed)/float64(total), fmt.Sprintf("scanning %d/%d", processed, total))
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if len(batch) > 0 {
		_ = upsertItemsBatch(ctx, s.Store, batch, domain.System)
	}
	for _, fid := range toQueue {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		subj := &database.Subject{Type: "file", ID: fid}
		params := map[string]any{"file_id": fid, "force": false}
		key := fmt.Sprintf("transcode:%d", fid)
		_ = s.enqueueJob(ctx, KindFile, subj, params, key)
	}
	_ = r.Progress(ctx, 1.0, "done")
	return nil
}
