package subtext

import (
	"fmt"
	"strings"
	"testing"
)

func buildSRT(count int, startSec, stepSec float64) string {
	var sb strings.Builder
	for i := range count {
		s := int64((startSec + float64(i)*stepSec) * 1000)
		e := s + 2000
		fmt.Fprintf(&sb, "%d\n%s --> %s\nDialogue line %d.\n\n", i+1, fmtCue(s, false), fmtCue(e, false), i+1)
	}
	return sb.String()
}

func TestCheckIntegrity_ValidFullMovie(t *testing.T) {
	// 1h 48m movie (6480s), cues spanning up to 98m (5880s).
	srt := buildSRT(600, 30, 9.75)
	rep := CheckIntegrity(srt, SRT, 6480, false)
	if !rep.OK || rep.Issue != IssueNone {
		t.Fatalf("expected OK, got %+v", rep)
	}
	if rep.Cues != 600 || rep.FirstMS != 30000 || rep.Coverage < 0.9 {
		t.Fatalf("unexpected report metrics: %+v", rep)
	}
}

func TestCheckIntegrity_Truncated(t *testing.T) {
	// 1h 48m movie (6480s), subtitle stops around 22:15 (1335s).
	srt := buildSRT(150, 10, 8.88)
	rep := CheckIntegrity(srt, SRT, 6480, false)
	if rep.OK || rep.Issue != IssueTruncated {
		t.Fatalf("expected truncated, got %+v", rep)
	}
	if !strings.Contains(rep.Reason, "1:48:00") || !strings.Contains(rep.Reason, "coverage") {
		t.Fatalf("unexpected reason: %q", rep.Reason)
	}
}

func TestCheckIntegrity_LongerThanVideo(t *testing.T) {
	// 1h 30m movie (5400s), subtitle runs past 2h 14m (8040s).
	srt := buildSRT(400, 10, 20.1)
	rep := CheckIntegrity(srt, SRT, 5400, false)
	if rep.OK || rep.Issue != IssueLongerThanVideo {
		t.Fatalf("expected longer_than_video, got %+v", rep)
	}
	if !strings.Contains(rep.Reason, "past the 1:30:00 video") {
		t.Fatalf("unexpected reason: %q", rep.Reason)
	}
}

func TestCheckIntegrity_TooFewCues(t *testing.T) {
	// 45m episode (2700s) with only 5 cues reaching the end.
	var sb strings.Builder
	for i := range 5 {
		s := int64((i*500 + 60) * 1000)
		e := s + 3000
		fmt.Fprintf(&sb, "%d\n%s --> %s\nLine %d\n\n", i+1, fmtCue(s, false), fmtCue(e, false), i+1)
	}
	rep := CheckIntegrity(sb.String(), SRT, 2700, false)
	if rep.OK || rep.Issue != IssueTooFewCues {
		t.Fatalf("expected too_few_cues, got %+v", rep)
	}
}

func TestCheckIntegrity_BrokenTiming(t *testing.T) {
	// Majority of cues have EndMS <= StartMS or empty text.
	cues := []Cue{
		{StartMS: 5000, EndMS: 4000, Text: "backwards"},
		{StartMS: -100, EndMS: 1000, Text: "negative"},
		{StartMS: 6000, EndMS: 7000, Text: "   "},
		{StartMS: 8000, EndMS: 9000, Text: "valid"},
	}
	rep := CheckCuesIntegrity(cues, 1800, false)
	if rep.OK || rep.Issue != IssueBrokenTiming {
		t.Fatalf("expected broken_timing, got %+v", rep)
	}
}

func TestCheckIntegrity_EmptyAndUnreadable(t *testing.T) {
	if rep := CheckIntegrity("   \n\t", SRT, 1800, false); rep.OK || rep.Issue != IssueEmpty {
		t.Fatalf("empty text: %+v", rep)
	}
	if rep := CheckIntegrity("WEBVTT\n\n", VTT, 1800, false); rep.OK || rep.Issue != IssueEmpty {
		t.Fatalf("header-only vtt: %+v", rep)
	}
	if rep := CheckIntegrity("some data", "idx", 1800, false); rep.OK || rep.Issue != IssueUnreadable {
		t.Fatalf("unsupported format: %+v", rep)
	}
	if rep := CheckIntegrity(strings.Repeat("x", MaxText+1), SRT, 1800, false); rep.OK || rep.Issue != IssueUnreadable {
		t.Fatalf("oversized text: %+v", rep)
	}
}

func TestCheckIntegrity_ForcedAndSubAndShortVideo(t *testing.T) {
	// Forced subtitles skip coverage and too-few checks, but still check timing/empty.
	shortSRT := buildSRT(3, 10, 20)
	if rep := CheckIntegrity(shortSRT, SRT, 5400, true); !rep.OK {
		t.Fatalf("forced should pass coverage/too-few: %+v", rep)
	}
	if rep := CheckIntegrity("", SRT, 5400, true); rep.OK || rep.Issue != IssueEmpty {
		t.Fatalf("forced empty should fail: %+v", rep)
	}

	// .sub skips cue-timestamp duration checks.
	if rep := CheckIntegrity("{1}{25}Hello", SUB, 5400, false); !rep.OK {
		t.Fatalf("sub should pass: %+v", rep)
	}

	// Short video (< 300s) skips coverage/too-few checks.
	if rep := CheckIntegrity(shortSRT, SRT, 120, false); !rep.OK {
		t.Fatalf("short video should pass: %+v", rep)
	}
}

func TestCheckIntegrity_ASSAndVTT(t *testing.T) {
	// 30m episode (1800s) with 30 cues in VTT and ASS reaching ~27m.
	var vtt strings.Builder
	vtt.WriteString("WEBVTT\n\n")
	var ass strings.Builder
	ass.WriteString("[Script Info]\nScriptType: v4.00+\n\n[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n")
	for i := range 30 {
		s := int64(i*55 + 10) * 1000
		e := s + 2000
		fmt.Fprintf(&vtt, "%s --> %s\nLine %d\n\n", fmtCue(s, true), fmtCue(e, true), i+1)
		fmt.Fprintf(&ass, "Dialogue: 0,%s,%s,Default,,0,0,0,,Line %d\n", fmtASS(s), fmtASS(e), i+1)
	}
	if rep := CheckIntegrity(vtt.String(), VTT, 1800, false); !rep.OK {
		t.Fatalf("vtt failed: %+v", rep)
	}
	if rep := CheckIntegrity(ass.String(), ASS, 1800, false); !rep.OK {
		t.Fatalf("ass failed: %+v", rep)
	}
}
