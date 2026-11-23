package transcode

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/jobs"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/importer"
	"github.com/HolyArrs/holyarrs/app/backend/internal/mediafiles"
)

// EventTranscoded is emitted when a library file finishes transcoding in place.
const EventTranscoded = "transcode_done"

var pidTempDirRe = regexp.MustCompile(`^holyarrs-transcode-(\d+)-`)

func sweepStaleScratchDirs(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	myPID := os.Getpid()
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m := pidTempDirRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		pid, err := strconv.Atoi(m[1])
		if err != nil || pid <= 0 || pid == myPID {
			continue
		}
		if !processAlive(pid) {
			_ = os.RemoveAll(filepath.Join(root, e.Name()))
		}
	}
}

func resolveScratchRoot(st Settings) (string, error) {
	root := strings.TrimSpace(st.ScratchDir)
	if root == "" {
		root = os.TempDir()
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	return root, nil
}

func replaceExtMKV(p string) string {
	ext := filepath.Ext(p)
	if ext == "" {
		return p + ".mkv"
	}
	return strings.TrimSuffix(p, ext) + ".mkv"
}

func replaceRelExtMKV(rel string) string {
	dot := strings.LastIndex(rel, ".")
	slash := strings.LastIndexAny(rel, "/\\")
	if dot <= slash || dot < 0 {
		return rel + ".mkv"
	}
	return rel[:dot] + ".mkv"
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(dst)
		return copyErr
	}
	if syncErr != nil {
		_ = os.Remove(dst)
		return syncErr
	}
	return closeErr
}

func moveFileCrossFS(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := copyFile(src, dst); err != nil {
		return err
	}
	return os.Remove(src)
}

func normalizeOrigLang(orig *string) string {
	if orig == nil {
		return ""
	}
	if iso, _, ok := NormalizeLanguage(*orig); ok {
		return iso
	}
	return ""
}

func effectiveConcurrentJobs(wanted int, gpus []GPUDevice) int {
	if wanted <= 1 {
		return 1
	}
	selectedCount := 0
	var onlyGPU GPUDevice
	for _, g := range gpus {
		if g.Selected {
			selectedCount++
			onlyGPU = g
		}
	}
	if selectedCount == 1 && onlyGPU.MemoryMB > 0 && onlyGPU.MemoryMB <= 6500 {
		return 1
	}
	return wanted
}

func shouldStageInWorkDir(targetDir, workDir string, origSize int64) (bool, int) {
	return shouldStageInWorkDirWith(targetDir, workDir, origSize, fsUsage)
}

func shouldStageInWorkDirWith(targetDir, workDir string, origSize int64, usageFn func(string) (uint64, int, string, bool)) (bool, int) {
	_, targetPct, targetFS, targetOK := usageFn(targetDir)
	workAvail, workPct, workFS, workOK := usageFn(workDir)
	minNeed := max(origSize*2, int64(10)<<30)
	if targetOK && workOK && targetPct >= 92 && targetFS != workFS && workPct < 85 && workAvail > uint64(minNeed) {
		return true, targetPct
	}
	return false, targetPct
}

func (s *Service) handleFileJob(ctx context.Context, r *jobs.Run) error {
	fileID, ok := fileIDParam(r.Job)
	if !ok {
		return fmt.Errorf("missing file_id parameter")
	}
	force, _ := r.Job.Params["force"].(bool)

	cur, done, err := s.loadFileJobState(ctx, fileID)
	if done {
		return nil
	}
	if err != nil {
		markItemError(ctx, s.Store, fileID, r.Job.ID, err.Error())
		return err
	}

	var hw HardwareInfo
	if !force {
		var skipped bool
		hw, skipped = s.tryFastSkip(ctx, cur.st, fileID, r.Job.ID, cur.prof.ID, cur.prof.ProfileConfig, cur.libEnabled, cur.inputPath, cur.origSize, cur.origMtimeNs)
		if skipped {
			return nil
		}
	}

	if err := s.acquireEncodeSlot(ctx, func() int {
		st := ReadSettings(ctx, s.Store.Read())
		hw := s.probeHardwareWithSettings(ctx, st)
		return effectiveConcurrentJobs(st.ConcurrentJobs, hw.GPUs)
	}); err != nil {
		return err
	}
	defer s.releaseEncodeSlot()

	fresh, done, err := s.loadFileJobState(ctx, fileID)
	if done {
		return nil
	}
	if err != nil {
		markItemError(ctx, s.Store, fileID, r.Job.ID, err.Error())
		return err
	}
	changed := fresh.prof.ID != cur.prof.ID ||
		fresh.libEnabled != cur.libEnabled ||
		fresh.inputPath != cur.inputPath ||
		fresh.origSize != cur.origSize ||
		fresh.origMtimeNs != cur.origMtimeNs ||
		!reflect.DeepEqual(fresh.prof.ProfileConfig, cur.prof.ProfileConfig)
	st := fresh.st
	fc := fresh.fc
	libEnabled := fresh.libEnabled
	prof := fresh.prof
	cfg := prof.ProfileConfig
	profID := prof.ID
	inputPath := fresh.inputPath
	origSize := fresh.origSize
	origMtimeNs := fresh.origMtimeNs
	if !force && changed {
		var skipped bool
		hw, skipped = s.tryFastSkip(ctx, st, fileID, r.Job.ID, profID, cfg, libEnabled, inputPath, origSize, origMtimeNs)
		if skipped {
			return nil
		}
	} else {
		hw = s.probeHardwareWithSettings(ctx, st)
	}
	effectiveJobs := effectiveConcurrentJobs(st.ConcurrentJobs, hw.GPUs)

	started := time.Now()
	defer s.clearLiveStat(r.Job.ID)

	if !hw.FFmpegFound || !hw.FFprobeFound {
		err := fmt.Errorf("ffmpeg or ffprobe binary not available")
		markItemError(ctx, s.Store, fileID, r.Job.ID, err.Error())
		return err
	}

	_ = r.Progress(ctx, 0.02, "probing")
	s.setLiveStat(r.Job.ID, liveStat{phase: "probing", profileName: prof.Name, inputBytes: origSize})

	probe, err := ProbeMedia(ctx, hw.FFprobePath, inputPath)
	if err != nil {
		markItemError(ctx, s.Store, fileID, r.Job.ID, err.Error())
		return err
	}

	relVideoIdx, vStream, hasVideo := MainVideoStream(probe.Streams)
	if !hasVideo {
		skip := "no_video"
		now := time.Now().UTC()
		_ = upsertItem(ctx, s.Store, &ItemRecord{
			FileID:          fileID,
			ProfileID:       &profID,
			State:           "skipped",
			SkipReason:      &skip,
			FileFlowsTagged: DetectSkipTag(probe, cfg.SkipTags),
			FileMtimeNs:     origMtimeNs,
			FileSizeBytes:   origSize,
			TargetCodec:     cfg.TargetCodec,
			InputBytes:      origSize,
			JobID:           &r.Job.ID,
			ProbedAt:        &now,
		}, domain.System)
		return nil
	}

	sourceHDR := DetectSourceHDR(vStream)
	fileFlowsTagged := DetectSkipTag(probe, cfg.SkipTags)

	if skip, skipReason := EvaluateProbe(probe, inputPath, origSize, cfg, libEnabled, force); skip {
		now := time.Now().UTC()
		_ = upsertItem(ctx, s.Store, &ItemRecord{
			FileID:          fileID,
			ProfileID:       &profID,
			State:           "skipped",
			SkipReason:      &skipReason,
			FileFlowsTagged: fileFlowsTagged,
			FileMtimeNs:     origMtimeNs,
			FileSizeBytes:   origSize,
			SourceCodec:     strings.ToLower(vStream.CodecName),
			SourceWidth:     vStream.Width,
			SourceHeight:    vStream.Height,
			SourceHDR:       sourceHDR,
			TargetCodec:     cfg.TargetCodec,
			InputBytes:      origSize,
			JobID:           &r.Job.ID,
			ProbedAt:        &now,
		}, domain.System)
		return nil
	}

	origLang := normalizeOrigLang(fc.Title.OriginalLanguage)
	keptAudio, keptSubs := SelectBestStreams(probe.Streams, origLang, cfg.AllowedLanguages, cfg.PreferVFQ)
	if cfg.RequireAudioStream && len(keptAudio) == 0 {
		err := fmt.Errorf("failed to find any wanted audio languages")
		markItemError(ctx, s.Store, fileID, r.Job.ID, err.Error())
		return err
	}

	scalePlan, err := ComputeScalePlan(vStream.Width, vStream.Height, vStream.SampleAspectRatio, cfg)
	if err != nil {
		markItemError(ctx, s.Store, fileID, r.Job.ID, err.Error())
		return err
	}
	tonemapToSDR, colorFlags := ResolveColorHandling(vStream, cfg.HDRMode)

	scratchRoot, err := resolveScratchRoot(st)
	if err != nil {
		markItemError(ctx, s.Store, fileID, r.Job.ID, err.Error())
		return err
	}
	sweepStaleScratchDirs(scratchRoot)
	workDir, err := os.MkdirTemp(scratchRoot, fmt.Sprintf("holyarrs-transcode-%d-", os.Getpid()))
	if err != nil {
		markItemError(ctx, s.Store, fileID, r.Job.ID, err.Error())
		return err
	}
	defer os.RemoveAll(workDir)

	duration := ParseDuration(probe, vStream)
	sourceFPS := ParseFPS(vStream.AvgFrameRate, 0)
	if sourceFPS <= 0 {
		sourceFPS = ParseFPS(vStream.RFrameRate, 24.0)
	}
	inputSize := ParseFormatSize(probe, origSize)
	inputVideoBytes := EstimateInputVideoBytes(probe, inputSize, duration)

	effectiveInput := inputPath
	effectiveDuration := duration
	hasEDLCuts := false
	if cfg.ApplyEDLCuts {
		edlPath := strings.TrimSuffix(inputPath, filepath.Ext(inputPath)) + ".edl"
		if keepSegs := ParseEDLKeepSegments(edlPath, duration); len(keepSegs) > 0 {
			_ = r.Progress(ctx, 0.04, "edl")
			s.setLiveStat(r.Job.ID, liveStat{phase: "edl", profileName: prof.Name, inputBytes: origSize})
			cutInput, cutDur, cutErr := ApplyEDLCutlist(ctx, hw.FFmpegPath, inputPath, probe.Streams, keepSegs, workDir)
			if cutErr != nil {
				markItemError(ctx, s.Store, fileID, r.Job.ID, cutErr.Error())
				return cutErr
			}
			effectiveInput = cutInput
			effectiveDuration = cutDur
			hasEDLCuts = true
			for i := range keptSubs {
				if needsMKVSubTranscode(keptSubs[i].CodecName) {
					keptSubs[i].CodecName = "subrip"
				}
			}
		}
	}

	enc := pickEffectiveEncoderForFile(cfg, st, hw)
	if enc == "" {
		err := fmt.Errorf("no usable encoder found for target_codec=%s", cfg.TargetCodec)
		markItemError(ctx, s.Store, fileID, r.Job.ID, err.Error())
		return err
	}
	decThreads, encThreads := ffmpegThreads(effectiveJobs)

	isAnime := IsAnimeMedia(inputPath, fc.Title.Genres, fc.Title.Tags)
	targetRel := replaceRelExtMKV(fc.File.RelativePath)
	targetPath := filepath.Join(fc.LibPath, filepath.FromSlash(targetRel))
	stagePath := filepath.Join(filepath.Dir(targetPath), fmt.Sprintf(".holyarrs-tc-%d-encoded.mkv.tmp", r.Job.ID))
	encodedTemp := filepath.Join(workDir, "encoded.mkv")
	sameFSStage := strings.TrimSpace(st.ScratchDir) == ""
	if sameFSStage {
		if mkErr := os.MkdirAll(filepath.Dir(stagePath), 0o755); mkErr != nil {
			markItemError(ctx, s.Store, fileID, r.Job.ID, mkErr.Error())
			return mkErr
		}
		if fallback, targetPct := shouldStageInWorkDir(filepath.Dir(targetPath), workDir, origSize); fallback {
			sameFSStage = false
			log.Printf("transcode: target filesystem is %d%% full, staging file %d in %s", targetPct, fileID, workDir)
		} else {
			encodedTemp = stagePath
		}
	}
	defer os.Remove(stagePath)
	mapArgs, tailArgs := BuildStreamMapsAndTail(isAnime, cfg, encodedTemp, relVideoIdx, keptAudio, keptSubs)

	var (
		gpuUsed           *GPUDevice
		gpuPathUsed       string
		selectedQ         *int
		meanVMAF          *float64
		copyFB            bool
		avgFPS            float64
		avgSpeed          float64
		encodePhase       = "encoding"
		lastProgressAt    time.Time
		lastProgressPhase string
	)

	setJobProgress := func(pct float64, phase string) {
		now := time.Now()
		if phase != lastProgressPhase || pct >= 1.0 || lastProgressAt.IsZero() || now.Sub(lastProgressAt) >= 2*time.Second {
			lastProgressAt = now
			lastProgressPhase = phase
			_ = r.Progress(ctx, pct, phase)
		}
	}

	updateEncodeProgress := func(u progressUpdate) {
		pct := 0.25
		if effectiveDuration > 0 && u.OutSeconds > 0 {
			frac := u.OutSeconds / effectiveDuration
			if frac > 1 {
				frac = 1
			}
			pct = 0.22 + frac*0.73
		}
		if u.FPS > 0 {
			avgFPS = u.FPS
		}
		if u.Speed > 0 {
			avgSpeed = u.Speed
		}
		s.setLiveStat(r.Job.ID, liveStat{
			phase:       encodePhase,
			encoder:     enc,
			gpuDevice:   gpuPathUsed,
			selectedQ:   selectedQ,
			meanVMAF:    meanVMAF,
			fps:         u.FPS,
			speed:       u.Speed,
			profileName: prof.Name,
			inputBytes:  origSize,
		})
		setJobProgress(pct, encodePhase)
	}

	encodeWithGPU := func() error {
		gpuUsed = nil
		gpuPathUsed = ""
		cardIdx := 0
		gpuVRAMMB := 0
		var releaseGPU func()
		if isDRMRenderEncoder(enc) {
			dev, rel, gOK := s.gpus.Acquire(hw.GPUs)
			if !gOK {
				return fmt.Errorf("no selected GPU render node available")
			}
			releaseGPU = rel
			gpuCopy := dev
			gpuUsed = &gpuCopy
			gpuPathUsed = dev.Path
			cardIdx = gpuCardIndex(hw.GPUs, dev.Path)
			gpuVRAMMB = dev.MemoryMB
		}
		if releaseGPU != nil {
			defer releaseGPU()
		}

		extraFrames, asyncDepth := qsvPipelineTuning(gpuVRAMMB, vStream.Height)
		reserveMB := estimateTranscodeVRAMMB(vStream.Height, extraFrames)
		s.setActiveGPUVRAM(cardIdx, reserveMB)
		defer s.setActiveGPUVRAM(cardIdx, -reserveMB)

		if s.YieldGPUVRAM != nil {
			s.YieldGPUVRAM(ctx, cardIdx, reserveMB)
		} else if s.YieldGPU != nil {
			s.YieldGPU(ctx)
		}

		setJobProgress(0.08, "vmaf")
		s.setLiveStat(r.Job.ID, liveStat{
			phase: "vmaf", encoder: enc, gpuDevice: gpuPathUsed, profileName: prof.Name, inputBytes: origSize,
		})
		searchRes, sErr := RunGPUVMAFSearch(
			ctx, hw, cfg, effectiveInput, relVideoIdx,
			scalePlan, tonemapToSDR, colorFlags, enc, gpuUsed,
			sourceFPS, effectiveDuration, inputVideoBytes, workDir, effectiveJobs,
		)
		if sErr != nil {
			return sErr
		}
		q := searchRes.SelectedQ
		v := searchRes.MeanVMAF
		selectedQ = &q
		meanVMAF = &v
		copyFB = searchRes.CopyVideoFallback

		tenBit := cfg.TenBit && cfg.TargetCodec != "h264"
		buildGPUArgs := func(allowHWDecode bool) ([]string, bool) {
			if copyFB {
				return []string{"-c:v", "copy"}, false
			}
			preHW, encFlags, usedHWD := hwPipelineArgs(hw, vStream, scalePlan, enc, gpuUsed, q, cfg.HWPreset, tenBit, tonemapToSDR, allowHWDecode)
			var out []string
			out = append(out, preHW...)
			out = append(out, encFlags...)
			out = append(out, "-g", "250")
			out = append(out, colorFlags...)
			return out, usedHWD
		}

		vEncArgs, usedHWDecode := buildGPUArgs(true)
		setJobProgress(0.22, encodePhase)
		log.Printf("transcode: encoding file %d with %s on %s (profile=%q hw_decode=%v preset=%s q=%d vmaf=%.2f target_vmaf=%.1f extra_frames=%d async_depth=%d)", fileID, enc, gpuPathUsed, prof.Name, usedHWDecode, cfg.HWPreset, q, v, cfg.VMAFMinScore, extraFrames, asyncDepth)
		cmdArgs := assembleFFmpegCmdWithThreads(effectiveInput, mapArgs, vEncArgs, tailArgs, decThreads, encThreads)
		_, runErr := runStreamingFFmpeg(ctx, gpuUsed, hw.FFmpegPath, cmdArgs, updateEncodeProgress)
		if runErr != nil && usedHWDecode && ctx.Err() == nil {
			log.Printf("transcode: hw decode failed for file %d (%s), retrying with software decode: %v", fileID, enc, runErr)
			_ = os.Remove(encodedTemp)
			s.setLiveStat(r.Job.ID, liveStat{
				phase: encodePhase, encoder: enc, gpuDevice: gpuPathUsed,
				selectedQ: selectedQ, meanVMAF: meanVMAF,
				profileName: prof.Name, inputBytes: origSize,
			})
			swArgs, _ := buildGPUArgs(false)
			swCmdArgs := assembleFFmpegCmdWithThreads(effectiveInput, mapArgs, swArgs, tailArgs, decThreads, encThreads)
			_, runErr = runStreamingFFmpeg(ctx, gpuUsed, hw.FFmpegPath, swCmdArgs, updateEncodeProgress)
		}
		return runErr
	}

	if isHardwareEncoder(enc) {
		err := encodeWithGPU()
		if err != nil {
			log.Printf("transcode: gpu encode failed for file %d (%s on %s): %v", fileID, enc, gpuPathUsed, err)
			if ctx.Err() == nil && strings.HasSuffix(enc, "_qsv") && effectiveEncoderPreference(cfg, st) == "auto" {
				vaapiEnc := strings.TrimSuffix(enc, "_qsv") + "_vaapi"
				if slices.Contains(hw.Encoders, vaapiEnc) {
					log.Printf("transcode: retrying file %d on GPU with %s after %s failure", fileID, vaapiEnc, enc)
					enc = vaapiEnc
					selectedQ = nil
					meanVMAF = nil
					copyFB = false
					_ = os.Remove(encodedTemp)
					err = encodeWithGPU()
					if err != nil {
						log.Printf("transcode: gpu encode failed for file %d (%s on %s): %v", fileID, enc, gpuPathUsed, err)
					}
				}
			}
		}
		if err != nil {
			if !st.AllowCPUFallback || ctx.Err() != nil {
				markItemError(ctx, s.Store, fileID, r.Job.ID, err.Error())
				return err
			}
			enc = cpuFallbackEncoderFor(cfg.TargetCodec)
			log.Printf("transcode: falling back to CPU %s for file %d after GPU error: %v", enc, fileID, err)
			gpuUsed = nil
			gpuPathUsed = ""
			selectedQ = nil
			meanVMAF = nil
			copyFB = false
			_ = os.Remove(encodedTemp)
			encodePhase = "cpu_fallback"
			setJobProgress(0.22, encodePhase)
			cpuArgs := buildCPUArgs(enc, cfg, st, scalePlan, tonemapToSDR, hw.TonemapxAvail, colorFlags, encThreads)
			log.Printf("transcode: encoding file %d with CPU %s (phase=%s threads=%d/%d)", fileID, enc, encodePhase, decThreads, encThreads)
			cmdArgs := assembleFFmpegCmdWithThreads(effectiveInput, mapArgs, cpuArgs, tailArgs, decThreads, encThreads)
			if _, cpuErr := runStreamingFFmpeg(ctx, nil, hw.FFmpegPath, cmdArgs, updateEncodeProgress); cpuErr != nil {
				markItemError(ctx, s.Store, fileID, r.Job.ID, cpuErr.Error())
				return cpuErr
			}
		}
	} else {
		setJobProgress(0.15, encodePhase)
		cpuArgs := buildCPUArgs(enc, cfg, st, scalePlan, tonemapToSDR, hw.TonemapxAvail, colorFlags, encThreads)
		log.Printf("transcode: encoding file %d with CPU %s (phase=%s threads=%d/%d)", fileID, enc, encodePhase, decThreads, encThreads)
		cmdArgs := assembleFFmpegCmdWithThreads(effectiveInput, mapArgs, cpuArgs, tailArgs, decThreads, encThreads)
		if _, cpuErr := runStreamingFFmpeg(ctx, nil, hw.FFmpegPath, cmdArgs, updateEncodeProgress); cpuErr != nil {
			markItemError(ctx, s.Store, fileID, r.Job.ID, cpuErr.Error())
			return cpuErr
		}
	}

	if encStat, statErr := os.Stat(encodedTemp); statErr == nil &&
		cfg.CopyIfLarger && scalePlan.AlreadyNormalized && !tonemapToSDR &&
		!copyFB && !hasEDLCuts && inputSize > 0 && encStat.Size() > inputSize {
		copyArgs := assembleFFmpegCmdWithThreads(effectiveInput, mapArgs, []string{"-c:v", "copy"}, tailArgs, decThreads, encThreads)
		if _, copyErr := runStreamingFFmpeg(ctx, nil, hw.FFmpegPath, copyArgs, updateEncodeProgress); copyErr != nil {
			markItemError(ctx, s.Store, fileID, r.Job.ID, copyErr.Error())
			return copyErr
		}
	}

	setJobProgress(0.96, "verifying")
	outProbe, verifyErr := ProbeMedia(ctx, hw.FFprobePath, encodedTemp)
	if verifyErr != nil {
		markItemError(ctx, s.Store, fileID, r.Job.ID, verifyErr.Error())
		return verifyErr
	}
	if _, _, hasOutVid := MainVideoStream(outProbe.Streams); !hasOutVid {
		err := fmt.Errorf("encoded output has no video stream")
		markItemError(ctx, s.Store, fileID, r.Job.ID, err.Error())
		return err
	}

	if !sameFSStage {
		if err := moveFileCrossFS(encodedTemp, stagePath); err != nil {
			markItemError(ctx, s.Store, fileID, r.Job.ID, err.Error())
			return err
		}
	}

	setJobProgress(0.98, "replacing")
	if err := s.atomicPromoteFile(ctx, fc, origSize, origMtimeNs, inputPath, stagePath, targetPath, targetRel, scalePlan, tonemapToSDR, cfg, profID, r.Job.ID, vStream, sourceHDR, selectedQ, meanVMAF, gpuPathUsed, hasEDLCuts, avgFPS, avgSpeed, started); err != nil {
		markItemError(ctx, s.Store, fileID, r.Job.ID, err.Error())
		return err
	}

	_ = r.Progress(ctx, 1.0, "done")
	if s.OnTranscoded != nil {
		s.OnTranscoded(ctx, fc.File.ID)
	}
	return nil
}

func (s *Service) atomicPromoteFile(
	ctx context.Context,
	fc mediafiles.TranscodeFileContext,
	origSize, origMtimeNs int64,
	inputPath, stagePath, targetPath, targetRel string,
	plan ScalePlan,
	tonemapToSDR bool,
	cfg ProfileConfig,
	profID, jobID int64,
	vStream ProbeStream,
	sourceHDR string,
	selectedQ *int,
	meanVMAF *float64,
	gpuDevice string,
	hasEDLCuts bool,
	avgFPS, avgSpeed float64,
	started time.Time,
) error {
	unlock := s.Store.LockFiles()
	curInfo, err := os.Stat(inputPath)
	if err != nil {
		unlock()
		return fmt.Errorf("source file disappeared before promote: %w", err)
	}
	if curInfo.Size() != origSize || curInfo.ModTime().UnixNano() != origMtimeNs {
		unlock()
		return fmt.Errorf("source file changed on disk during transcode")
	}

	var holdDest string
	if cfg.KeepOriginalHolding {
		var holdErr error
		holdDest, holdErr = importer.HoldPath(fc.LibPath, fc.File.RelativePath, time.Now().UTC())
		if holdErr != nil {
			unlock()
			return fmt.Errorf("compute holding path: %w", holdErr)
		}
		if err := moveFileCrossFS(inputPath, holdDest); err != nil {
			unlock()
			return fmt.Errorf("move original to holding: %w", err)
		}
	}

	if err := os.Rename(stagePath, targetPath); err != nil {
		if holdDest != "" {
			_ = moveFileCrossFS(holdDest, inputPath)
		}
		unlock()
		return fmt.Errorf("promote transcoded file: %w", err)
	}
	if !cfg.KeepOriginalHolding && targetPath != inputPath {
		_ = os.Remove(inputPath)
	}

	finalStat, statErr := os.Stat(targetPath)
	if statErr != nil {
		unlock()
		return fmt.Errorf("stat promoted output: %w", statErr)
	}
	unlock()

	facts := importer.InspectFacts(targetPath)
	langs := importer.InspectedLanguages(facts)
	qual := updatedTranscodeQuality(fc.File.Quality, cfg.TargetCodec, plan.FrameH, tonemapToSDR)

	now := time.Now().UTC()
	outSize := finalStat.Size()
	outMtimeNs := finalStat.ModTime().UnixNano()
	saved := origSize - outSize
	elapsed := time.Since(started).Seconds()
	if avgFPS <= 0 && elapsed > 0 && vStream.Duration != "" {
		if dur, err := strconv.ParseFloat(vStream.Duration, 64); err == nil && dur > 0 {
			avgSpeed = dur / elapsed
		}
	}

	err = s.Store.Write(ctx, func(tx *sql.Tx) error {
		if err := mediafiles.UpdateTranscodedFileTx(ctx, tx, fc.File.ID, fc.Version.ID, targetRel, outSize, qual, langs, facts, domain.System); err != nil {
			return err
		}
		rec := &ItemRecord{
			FileID:          fc.File.ID,
			ProfileID:       &profID,
			State:           "done",
			FileFlowsTagged: strings.TrimSpace(cfg.WriteTag) != "",
			FileMtimeNs:     outMtimeNs,
			FileSizeBytes:   outSize,
			SourceCodec:     strings.ToLower(vStream.CodecName),
			SourceWidth:     vStream.Width,
			SourceHeight:    vStream.Height,
			SourceHDR:       sourceHDR,
			TargetCodec:     cfg.TargetCodec,
			SelectedQ:       selectedQ,
			MeanVMAF:        meanVMAF,
			GPUDevice:       gpuDevice,
			InputBytes:      origSize,
			OutputBytes:     outSize,
			SavedBytes:      saved,
			EncodeSeconds:   elapsed,
			AvgFPS:          avgFPS,
			AvgSpeed:        avgSpeed,
			EDLApplied:      hasEDLCuts,
			Tonemapped:      tonemapToSDR,
			JobID:           &jobID,
			ProbedAt:        &now,
			CompletedAt:     &now,
		}
		return upsertItemTx(ctx, tx, rec, domain.System)
	})
	if err != nil {
		return err
	}

	titleID := fc.Title.ID
	ev := &domain.Event{
		Actor:   domain.System,
		Kind:    EventTranscoded,
		TitleID: &titleID,
		Summary: fmt.Sprintf("Transcoded %s to %s", filepath.Base(targetRel), strings.ToUpper(cfg.TargetCodec)),
		Data: map[string]any{
			"file_id":           fc.File.ID,
			"slot_ids":          fc.File.SlotIDs,
			"library_folder_id": fc.Version.LibraryFolderID,
			"target_codec":      cfg.TargetCodec,
			"saved_bytes":       saved,
			"paths":             []string{targetPath},
		},
	}
	if s.Store.AddEvent(ctx, ev) == nil && s.Live != nil {
		s.Live.Publish("event", ev)
	}
	return nil
}

func updatedTranscodeQuality(q mediafiles.Quality, targetCodec string, frameH int, tonemapped bool) mediafiles.Quality {
	switch strings.ToLower(targetCodec) {
	case "hevc":
		q.Codec = "x265"
	case "h264":
		q.Codec = "x264"
	default:
		q.Codec = "AV1"
	}
	if frameH >= 2000 {
		q.Resolution = 2160
	} else if frameH >= 900 {
		q.Resolution = 1080
	} else if frameH >= 600 {
		q.Resolution = 720
	}
	if tonemapped {
		q.HDR = nil
	}
	return q
}

func fileIDParam(j jobs.Job) (int64, bool) {
	if j.Subject != nil && j.Subject.Type == "file" && j.Subject.ID > 0 {
		return j.Subject.ID, true
	}
	if v, ok := numFromAny(j.Params["file_id"]); ok && v > 0 {
		return v, true
	}
	return 0, false
}

func numFromAny(v any) (int64, bool) {
	switch x := v.(type) {
	case float64:
		return int64(x), true
	case int64:
		return x, true
	case int:
		return int64(x), true
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return n, err == nil
	}
	return 0, false
}
