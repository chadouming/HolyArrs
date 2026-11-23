package transcode

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/mediafiles"
)

const fastReadWindowSize = 64 * 1024

var errFastUnsupported = errors.New("unsupported container for fast probe")

// FastProbeResult holds lightweight container header and metadata tag values.
type FastProbeResult struct {
	HasVideo     bool
	SourceCodec  string
	SourceWidth  int
	SourceHeight int
	SourceHDR    string
	Tags         []string
	UsedFallback bool
}

// EvaluateFastProbe decides whether a fast-probed file should be skipped under cfg.
func EvaluateFastProbe(fp FastProbeResult, mediaPath string, fileSizeBytes int64, cfg ProfileConfig, libraryEnabled, force bool) (bool, string) {
	return evaluateSkipDecision(fp.HasVideo, fp.SourceCodec, fp.Tags, mediaPath, fileSizeBytes, cfg, libraryEnabled, force)
}

// FastProbeFile extracts main video track headers and FileFlows comment tags using pure Go for
// Matroska/WebM and MP4/MOV, falling back to bounded ffprobe only when needed.
func FastProbeFile(ctx context.Context, ffprobeBin, mediaPath string) (FastProbeResult, error) {
	f, err := os.Open(mediaPath)
	if err != nil {
		return FastProbeResult{}, err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return FastProbeResult{}, err
	}
	if !st.Mode().IsRegular() {
		return FastProbeResult{}, fmt.Errorf("fast probe %s: not a regular file", mediaPath)
	}
	size := st.Size()
	if size >= 8 {
		br := newBufferedReaderAt(f, size)
		var prefix [8]byte
		if n, rErr := br.ReadAt(prefix[:], 0); rErr == nil || (errors.Is(rErr, io.EOF) && n >= 4) {
			if string(prefix[:4]) == "\x1a\x45\xdf\xa3" {
				if res, mErr := probeMatroskaFast(br, size); mErr == nil {
					return res, nil
				}
			} else if looksLikeMP4Header(prefix[:n]) {
				if res, pErr := probeMP4Fast(br, size); pErr == nil {
					return res, nil
				}
			}
		}
	}

	if ffprobeBin == "" {
		return FastProbeResult{}, fmt.Errorf("fast probe %s: unable to parse container and ffprobe not available", mediaPath)
	}
	return probeBoundedFFprobe(ctx, ffprobeBin, mediaPath)
}

func (s *Service) tryFastSkip(ctx context.Context, st Settings, fileID, jobID, profID int64, cfg ProfileConfig, libEnabled bool, inputPath string, origSize, origMtimeNs int64) (HardwareInfo, bool) {
	if rec, found, gErr := getItemByFileID(ctx, s.Store.Read(), fileID); gErr == nil && found &&
		rec.State == "done" && sameFileOnDisk(rec, origSize, origMtimeNs) &&
		(rec.TargetCodec == "" || rec.TargetCodec == cfg.TargetCodec) {
		return HardwareInfo{}, true
	}
	hw := s.probeHardwareWithSettings(ctx, st)
	if fp, fpErr := FastProbeFile(ctx, hw.FFprobePath, inputPath); fpErr == nil {
		if skip, skipReason := EvaluateFastProbe(fp, inputPath, origSize, cfg, libEnabled, false); skip {
			now := time.Now().UTC()
			_ = upsertItem(ctx, s.Store, &ItemRecord{
				FileID:          fileID,
				ProfileID:       &profID,
				State:           "skipped",
				SkipReason:      &skipReason,
				FileFlowsTagged: MatchSkipTags(fp.Tags, cfg.SkipTags),
				FileMtimeNs:     origMtimeNs,
				FileSizeBytes:   origSize,
				SourceCodec:     fp.SourceCodec,
				SourceWidth:     fp.SourceWidth,
				SourceHeight:    fp.SourceHeight,
				SourceHDR:       fp.SourceHDR,
				TargetCodec:     cfg.TargetCodec,
				InputBytes:      origSize,
				JobID:           &jobID,
				ProbedAt:        &now,
			}, domain.System)
			return hw, true
		}
	} else if !libEnabled {
		skipReason := "library_disabled"
		now := time.Now().UTC()
		_ = upsertItem(ctx, s.Store, &ItemRecord{
			FileID:        fileID,
			ProfileID:     &profID,
			State:         "skipped",
			SkipReason:    &skipReason,
			FileMtimeNs:   origMtimeNs,
			FileSizeBytes: origSize,
			TargetCodec:   cfg.TargetCodec,
			InputBytes:    origSize,
			JobID:         &jobID,
			ProbedAt:      &now,
		}, domain.System)
		return hw, true
	}
	return hw, false
}

func looksLikeMP4Header(prefix []byte) bool {
	if len(prefix) < 8 {
		return false
	}
	switch string(prefix[4:8]) {
	case "ftyp", "moov", "mdat", "wide", "free", "skip", "pnot":
		return true
	default:
		return false
	}
}

func probeMP4Fast(r io.ReaderAt, size int64) (FastProbeResult, error) {
	info, err := mediafiles.InspectReaderAt(r, size)
	if err != nil || info.Status == "unsupported" {
		return FastProbeResult{}, errFastUnsupported
	}
	tags := probeMP4FastTags(r, size)
	for _, tr := range info.Tracks {
		if tr.Kind != "video" {
			continue
		}
		if tr.Format == "" {
			return FastProbeResult{}, errFastUnsupported
		}
		return FastProbeResult{
			HasVideo:     true,
			SourceCodec:  strings.ToLower(tr.Format),
			SourceWidth:  tr.Width,
			SourceHeight: tr.Height,
			SourceHDR:    mapMediafilesHDR(tr.HDR),
			Tags:         tags,
		}, nil
	}
	if len(info.Tracks) > 0 {
		return FastProbeResult{HasVideo: false, Tags: tags}, nil
	}
	return FastProbeResult{}, errFastUnsupported
}

func probeMP4FastTags(r io.ReaderAt, size int64) []string {
	var tags []string
	pos := int64(0)
	for i := 0; i < 256 && pos < size && len(tags) < fastMaxTagStrings; i++ {
		boxType, pStart, bEnd, ok := readMP4BoxHeader(r, pos, size)
		if !ok {
			break
		}
		if boxType == "moov" {
			collectMP4MoovTags(r, pStart, bEnd, &tags)
			break
		}
		pos = bEnd
	}
	return tags
}

func collectMP4MoovTags(r io.ReaderAt, start, end int64, tags *[]string) {
	pos := start
	for i := 0; i < 128 && pos < end && len(*tags) < fastMaxTagStrings; i++ {
		boxType, pStart, bEnd, ok := readMP4BoxHeader(r, pos, end)
		if !ok {
			break
		}
		switch boxType {
		case "udta":
			collectMP4UdtaTags(r, pStart, bEnd, tags)
		case "meta":
			collectMP4MetaTags(r, pStart, bEnd, tags)
		}
		pos = bEnd
	}
}

func collectMP4UdtaTags(r io.ReaderAt, start, end int64, tags *[]string) {
	pos := start
	for i := 0; i < 64 && pos < end && len(*tags) < fastMaxTagStrings; i++ {
		boxType, pStart, bEnd, ok := readMP4BoxHeader(r, pos, end)
		if !ok {
			break
		}
		if boxType == "meta" {
			collectMP4MetaTags(r, pStart, bEnd, tags)
		} else if len(boxType) == 4 && boxType[0] == 0xa9 && bEnd-pStart > 4 {
			readLen := min64(bEnd-pStart, 1024)
			if buf, err := fastReadAt(r, pStart, readLen); err == nil && len(buf) >= 4 {
				strLen := int(binary.BigEndian.Uint16(buf[:2]))
				if strLen > 0 && 4+strLen <= len(buf) {
					s := strings.TrimSpace(strings.ToValidUTF8(strings.TrimRight(string(buf[4:4+strLen]), "\x00"), ""))
					if s != "" {
						*tags = appendCappedTags(*tags, []string{s}, fastMaxTagStrings)
					}
				}
			}
		}
		pos = bEnd
	}
}

func collectMP4MetaTags(r io.ReaderAt, start, end int64, tags *[]string) {
	childStart := start
	if start+4 < end {
		if _, _, _, ok := readMP4BoxHeader(r, start+4, end); ok {
			childStart = start + 4
		}
	}
	pos := childStart
	for i := 0; i < 64 && pos < end && len(*tags) < fastMaxTagStrings; i++ {
		boxType, pStart, bEnd, ok := readMP4BoxHeader(r, pos, end)
		if !ok {
			break
		}
		if boxType == "ilst" {
			collectMP4IlstTags(r, pStart, bEnd, tags)
		}
		pos = bEnd
	}
}

func collectMP4IlstTags(r io.ReaderAt, start, end int64, tags *[]string) {
	pos := start
	for i := 0; i < 64 && pos < end && len(*tags) < fastMaxTagStrings; i++ {
		_, pStart, bEnd, ok := readMP4BoxHeader(r, pos, end)
		if !ok {
			break
		}
		subPos := pStart
		for j := 0; j < 16 && subPos < bEnd && len(*tags) < fastMaxTagStrings; j++ {
			subType, spStart, sbEnd, subOK := readMP4BoxHeader(r, subPos, bEnd)
			if !subOK {
				break
			}
			if subType == "data" && sbEnd-spStart > 8 {
				readLen := min64(sbEnd-spStart, 1024)
				if buf, err := fastReadAt(r, spStart, readLen); err == nil && len(buf) > 8 {
					dataType := binary.BigEndian.Uint32(buf[:4])
					if dataType == 1 || dataType == 0 {
						s := strings.TrimSpace(strings.ToValidUTF8(strings.TrimRight(string(buf[8:]), "\x00"), ""))
						if s != "" {
							*tags = appendCappedTags(*tags, []string{s}, fastMaxTagStrings)
						}
					}
				}
			}
			subPos = sbEnd
		}
		pos = bEnd
	}
}

func readMP4BoxHeader(r io.ReaderAt, pos, end int64) (string, int64, int64, bool) {
	if pos < 0 || pos+8 > end {
		return "", 0, 0, false
	}
	hdr, err := fastReadAt(r, pos, 8)
	if err != nil || len(hdr) < 8 {
		return "", 0, 0, false
	}
	sz := uint64(binary.BigEndian.Uint32(hdr[:4]))
	boxType := string(hdr[4:8])
	hdrLen := int64(8)
	var boxEnd int64
	switch sz {
	case 0:
		boxEnd = end
	case 1:
		if pos+16 > end {
			return "", 0, 0, false
		}
		ext, err := fastReadAt(r, pos+8, 8)
		if err != nil || len(ext) < 8 {
			return "", 0, 0, false
		}
		extSz := binary.BigEndian.Uint64(ext)
		if extSz < 16 || extSz > uint64(end-pos) {
			return "", 0, 0, false
		}
		hdrLen = 16
		boxEnd = pos + int64(extSz)
	default:
		if sz < 8 || sz > uint64(end-pos) {
			return "", 0, 0, false
		}
		boxEnd = pos + int64(sz)
	}
	return boxType, pos + hdrLen, boxEnd, true
}

func mapMediafilesHDR(hdr string) string {
	switch hdr {
	case "HDR10":
		return "hdr10"
	case "HLG":
		return "hlg"
	case "DV":
		return "dv5"
	case "DV+HDR10":
		return "dv"
	default:
		return ""
	}
}

func probeBoundedFFprobe(ctx context.Context, ffprobeBin, mediaPath string) (FastProbeResult, error) {
	res, err := runSubproc(ctx, nil, ffprobeBin,
		"-v", "quiet",
		"-probesize", "65536",
		"-analyzeduration", "100000",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		mediaPath,
	)
	if err != nil {
		return FastProbeResult{}, fmt.Errorf("ffprobe %s: %w (%s)", mediaPath, err, strings.TrimSpace(res.Stderr))
	}
	var out ProbeResult
	if err := json.Unmarshal([]byte(res.Stdout), &out); err != nil {
		return FastProbeResult{}, fmt.Errorf("parse ffprobe json for %s: %w", mediaPath, err)
	}
	fp := FastProbeResult{
		Tags:         ProbeTagValues(out),
		UsedFallback: true,
	}
	if _, v, ok := MainVideoStream(out.Streams); ok {
		fp.HasVideo = true
		fp.SourceCodec = strings.ToLower(v.CodecName)
		fp.SourceWidth = v.Width
		fp.SourceHeight = v.Height
		fp.SourceHDR = DetectSourceHDR(v)
	}
	return fp, nil
}

type bufferedReaderAt struct {
	r      io.ReaderAt
	size   int64
	buf    [fastReadWindowSize]byte
	start  int64
	length int64
}

func newBufferedReaderAt(r io.ReaderAt, size int64) *bufferedReaderAt {
	return &bufferedReaderAt{r: r, size: size, start: -1}
}

func (b *bufferedReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= b.size {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	want := int64(len(p))
	if off+want > b.size {
		want = b.size - off
	}
	if b.start >= 0 && off >= b.start && off+want <= b.start+b.length {
		copy(p[:want], b.buf[off-b.start:off-b.start+want])
		if want < int64(len(p)) {
			return int(want), io.EOF
		}
		return int(want), nil
	}
	if want > int64(len(b.buf)) {
		n, err := b.r.ReadAt(p[:want], off)
		if err == nil && int64(n) < int64(len(p)) {
			return n, io.EOF
		}
		return n, err
	}
	win := min64(int64(len(b.buf)), b.size-off)
	n, err := b.r.ReadAt(b.buf[:win], off)
	if n > 0 {
		b.start = off
		b.length = int64(n)
		copied := min(int(want), n)
		copy(p[:copied], b.buf[:copied])
		if copied < len(p) {
			if err != nil {
				return copied, err
			}
			return copied, io.EOF
		}
		return copied, nil
	}
	return 0, err
}
