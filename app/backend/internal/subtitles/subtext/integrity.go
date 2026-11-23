package subtext

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// IntegrityIssue classifies why a subtitle failed integrity verification.
type IntegrityIssue string

const (
	IssueNone            IntegrityIssue = ""
	IssueUnreadable      IntegrityIssue = "unreadable"
	IssueEmpty           IntegrityIssue = "empty"
	IssueBrokenTiming    IntegrityIssue = "broken_timing"
	IssueTooFewCues      IntegrityIssue = "too_few_cues"
	IssueTruncated       IntegrityIssue = "truncated"
	IssueLongerThanVideo IntegrityIssue = "longer_than_video"
)

// IntegrityReport describes the outcome of checking a subtitle against its video duration.
type IntegrityReport struct {
	OK           bool           `json:"ok"`
	Issue        IntegrityIssue `json:"issue,omitempty"`
	Reason       string         `json:"reason,omitempty"`
	Cues         int            `json:"cues"`
	FirstMS      int64          `json:"first_ms"`
	LastMS       int64          `json:"last_ms"`
	VideoSeconds float64        `json:"video_seconds,omitempty"`
	Coverage     float64        `json:"coverage,omitempty"`
}

// CheckIntegrity parses subtitle text in format and validates its structure and
// duration against videoSeconds.
func CheckIntegrity(text, format string, videoSeconds float64, forced bool) IntegrityReport {
	fmtName := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(format), "."))
	if strings.TrimSpace(text) == "" {
		return IntegrityReport{
			Issue:        IssueEmpty,
			Reason:       "Subtitle file is empty",
			VideoSeconds: videoSeconds,
		}
	}
	if fmtName == SUB {
		return IntegrityReport{
			OK:           true,
			VideoSeconds: videoSeconds,
		}
	}
	cues, err := ParseCues(text, fmtName)
	if err != nil {
		if errors.Is(err, ErrNotSubtitle) {
			return IntegrityReport{
				Issue:        IssueEmpty,
				Reason:       "Subtitle contains no timed cues",
				VideoSeconds: videoSeconds,
			}
		}
		return IntegrityReport{
			Issue:        IssueUnreadable,
			Reason:       err.Error(),
			VideoSeconds: videoSeconds,
		}
	}
	return CheckCuesIntegrity(cues, videoSeconds, forced)
}

// CheckCuesIntegrity validates parsed cues against videoSeconds.
func CheckCuesIntegrity(cues []Cue, videoSeconds float64, forced bool) IntegrityReport {
	rep := IntegrityReport{
		Cues:         len(cues),
		VideoSeconds: videoSeconds,
	}
	if len(cues) == 0 {
		rep.Issue = IssueEmpty
		rep.Reason = "Subtitle contains no cues"
		return rep
	}

	firstMS := cues[0].StartMS
	lastMS := cues[0].EndMS
	bad := 0
	for _, c := range cues {
		if c.StartMS < firstMS {
			firstMS = c.StartMS
		}
		if c.EndMS > lastMS {
			lastMS = c.EndMS
		}
		if c.EndMS <= c.StartMS || c.StartMS < 0 || strings.TrimSpace(c.Text) == "" {
			bad++
		}
	}
	rep.FirstMS = firstMS
	rep.LastMS = lastMS

	if bad*2 > len(cues) {
		rep.Issue = IssueBrokenTiming
		rep.Reason = fmt.Sprintf("%d of %d cues have invalid timing or empty text", bad, len(cues))
		return rep
	}
	if lastMS <= 0 {
		rep.Issue = IssueBrokenTiming
		rep.Reason = "Subtitle has no positive end timestamp"
		return rep
	}

	if !forced && videoSeconds >= 300 {
		credits := min(max(videoSeconds*0.12, 120), 720)
		effective := max(videoSeconds-credits, videoSeconds*0.75)
		lastSec := float64(lastMS) / 1000.0
		coverage := lastSec / effective
		rep.Coverage = math.Round(coverage*1000) / 1000

		if videoSeconds > 600 && len(cues) < 10 {
			rep.Issue = IssueTooFewCues
			rep.Reason = fmt.Sprintf("Subtitle has only %d cues for a %s video", len(cues), fmtDuration(videoSeconds))
			return rep
		}
		if lastSec < effective*0.70 {
			pct := int(math.Round(coverage * 100))
			rep.Issue = IssueTruncated
			rep.Reason = fmt.Sprintf("Subtitle ends at %s of a %s video (%d%% coverage)", fmtDuration(lastSec), fmtDuration(videoSeconds), pct)
			return rep
		}
		if lastSec > videoSeconds*1.15+120 {
			rep.Issue = IssueLongerThanVideo
			rep.Reason = fmt.Sprintf("Subtitle runs to %s, past the %s video", fmtDuration(lastSec), fmtDuration(videoSeconds))
			return rep
		}
	}

	rep.OK = true
	return rep
}

func fmtDuration(sec float64) string {
	total := int(math.Round(max(sec, 0)))
	if total >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", total/3600, (total%3600)/60, total%60)
	}
	return fmt.Sprintf("%d:%02d", total/60, total%60)
}
