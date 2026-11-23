package transcode

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// KeepSegment defines one retained interval [Start, End] in seconds.
type KeepSegment struct {
	Start float64
	End   float64
}

func round3(v float64) float64 {
	return math.Round(v*1000.0) / 1000.0
}

// ParseEDLKeepSegments parses an MPlayer/Kodi/Comskip .edl file and returns retained intervals.
func ParseEDLKeepSegments(edlPath string, totalDuration float64) []KeepSegment {
	if totalDuration <= 0 {
		return nil
	}
	data, err := os.ReadFile(edlPath)
	if err != nil {
		return nil
	}
	type cut struct{ start, end float64 }
	var cuts []cut
	for _, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		s, err1 := strconv.ParseFloat(fields[0], 64)
		e, err2 := strconv.ParseFloat(fields[1], 64)
		act, err3 := strconv.Atoi(fields[2])
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		if s < 0 {
			s = 0
		}
		if e > totalDuration {
			e = totalDuration
		}
		if act == 0 && e > s {
			cuts = append(cuts, cut{start: s, end: e})
		}
	}
	if len(cuts) == 0 {
		return nil
	}
	sort.Slice(cuts, func(i, j int) bool { return cuts[i].start < cuts[j].start })
	var merged []cut
	for _, c := range cuts {
		if len(merged) == 0 || c.start > merged[len(merged)-1].end {
			merged = append(merged, c)
		} else if c.end > merged[len(merged)-1].end {
			merged[len(merged)-1].end = c.end
		}
	}

	var keep []KeepSegment
	cursor := 0.0
	for _, c := range merged {
		if c.start-cursor >= 0.05 {
			keep = append(keep, KeepSegment{Start: round3(cursor), End: round3(c.start)})
		}
		if c.end > cursor {
			cursor = c.end
		}
	}
	if totalDuration-cursor >= 0.05 {
		keep = append(keep, KeepSegment{Start: round3(cursor), End: round3(totalDuration)})
	}
	return keep
}

// ApplyEDLCutlist cuts retained segments without data streams and joins them via ffconcat.
func ApplyEDLCutlist(
	ctx context.Context,
	ffmpegBin string,
	inputPath string,
	streams []ProbeStream,
	keep []KeepSegment,
	workDir string,
) (string, float64, error) {
	var subCodecArgs []string
	subIdx := 0
	for _, st := range streams {
		if st.CodecType != "subtitle" {
			continue
		}
		codec := "copy"
		if needsMKVSubTranscode(st.CodecName) {
			codec = "srt"
		}
		subCodecArgs = append(subCodecArgs, fmt.Sprintf("-c:s:%d", subIdx), codec)
		subIdx++
	}

	var parts []string
	var keptDur float64
	for i, seg := range keep {
		partPath := filepath.Join(workDir, fmt.Sprintf("edl_keep_%03d.mkv", i))
		args := []string{
			"-hide_banner", "-y", "-nostats",
			"-ss", fmt.Sprintf("%.3f", seg.Start),
			"-to", fmt.Sprintf("%.3f", seg.End),
			"-i", inputPath,
			"-map", "0:v", "-map", "0:a?", "-map", "0:s?", "-map", "0:t?",
			"-c:v", "copy", "-c:a", "copy",
		}
		args = append(args, subCodecArgs...)
		args = append(args, "-c:t", "copy", partPath)

		res, err := runSubproc(ctx, nil, ffmpegBin, args...)
		if err != nil {
			return "", 0, fmt.Errorf("edl cut segment %d: %w (%s)", i, err, strings.TrimSpace(res.Stderr))
		}
		parts = append(parts, partPath)
		keptDur += seg.End - seg.Start
	}

	if len(parts) == 1 {
		return parts[0], keptDur, nil
	}

	concatTxt := filepath.Join(workDir, "edl_concat.ffconcat")
	var b strings.Builder
	b.WriteString("ffconcat version 1.0\n")
	for _, p := range parts {
		escaped := strings.ReplaceAll(p, "'", `'\''`)
		b.WriteString("file '")
		b.WriteString(escaped)
		b.WriteString("'\n")
	}
	if err := os.WriteFile(concatTxt, []byte(b.String()), 0o600); err != nil {
		return "", 0, fmt.Errorf("write ffconcat: %w", err)
	}

	joinedPath := filepath.Join(workDir, "edl_joined.mkv")
	res, err := runSubproc(ctx, nil, ffmpegBin,
		"-hide_banner", "-y", "-nostats",
		"-f", "concat", "-safe", "0", "-i", concatTxt,
		"-map", "0:v", "-map", "0:a?", "-map", "0:s?", "-map", "0:t?",
		"-c", "copy", joinedPath,
	)
	if err != nil {
		return "", 0, fmt.Errorf("edl concat: %w (%s)", err, strings.TrimSpace(res.Stderr))
	}
	return joinedPath, keptDur, nil
}
