package subtext

import (
	"strings"
	"testing"
)

func TestParseCues_SRTAndBack(t *testing.T) {
	in := "1\r\n00:00:01,000 --> 00:00:02,500\r\n<i>Hello</i>\r\nthere\r\n\r\n2\r\n00:00:03,000 --> 00:00:04,000\r\nBye\r\n"
	cues, err := ParseCues(in, SRT)
	if err != nil || len(cues) != 2 || cues[0].Text != "<i>Hello</i>\nthere" || cues[0].StartMS != 1000 || cues[1].EndMS != 4000 {
		t.Fatalf("%+v %v", cues, err)
	}
	out := WriteSRT(cues)
	want := "1\n00:00:01,000 --> 00:00:02,500\n<i>Hello</i>\nthere\n\n2\n00:00:03,000 --> 00:00:04,000\nBye\n\n"
	if out != want {
		t.Fatalf("written %q", out)
	}
	again, err := ParseCues(out, SRT)
	if err != nil || len(again) != 2 || again[0] != cues[0] {
		t.Fatalf("round trip %+v %v", again, err)
	}
}

func TestParseCues_VTT(t *testing.T) {
	in := "WEBVTT\n\nNOTE made up\n\nintro\n00:01.000 --> 00:02.000 align:start\nOne\n\n00:00:03.000 --> 00:00:04.000\nTwo\n"
	cues, err := ParseCues(in, VTT)
	if err != nil || len(cues) != 2 || cues[0].Text != "One" || cues[1].StartMS != 3000 {
		t.Fatalf("%+v %v", cues, err)
	}
}

func TestParseCues_ASSKeepsStyleAndBreaks(t *testing.T) {
	in := "[Script Info]\nTitle: lab\n\n[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n" +
		"Dialogue: 0,0:00:05.00,0:00:06.50,Default,,0,0,0,,{\\an8\\i1}Up here{\\i0}\\Nnext, line\n" +
		"Comment: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,not shown\n" +
		"Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,First\\hword\n"
	cues, err := ParseCues(in, ASS)
	if err != nil || len(cues) != 2 {
		t.Fatalf("%+v %v", cues, err)
	}
	if cues[0].Text != "First word" || cues[1].Text != "<i>Up here</i>\nnext, line" || cues[1].EndMS != 6500 {
		t.Fatalf("%+v", cues)
	}
}

func TestParseCues_Refusals(t *testing.T) {
	if _, err := ParseCues("no cues here", SRT); err == nil {
		t.Error("text without cues was read")
	}
	if _, err := ParseCues("{1}{2}x", SUB); err == nil {
		t.Error("microdvd was read as cues")
	}
	if _, err := ParseCues(strings.Repeat("x", MaxText+1), SRT); err == nil {
		t.Error("oversized text was read")
	}
	// A Format line naming fewer fields than Start and End need is refused, not
	// read past its end.
	if _, err := ParseCues("Format:Text\nDialogue:0", ASS); err == nil {
		t.Error("a dialogue without its times was read")
	}
}

func TestWriteSRT_SkipsEmptyAndBlankLines(t *testing.T) {
	out := WriteSRT([]Cue{{StartMS: 0, EndMS: 1000, Text: " "}, {StartMS: 1000, EndMS: 2000, Text: "a\n\nb"}})
	if out != "1\n00:00:01,000 --> 00:00:02,000\na\nb\n\n" {
		t.Fatalf("%q", out)
	}
}

func FuzzParseCues(f *testing.F) {
	f.Add("1\n00:00:01,000 --> 00:00:02,000\nHi\n", SRT)
	f.Add("Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\nDialogue: 0,0:00:01.00,0:00:02.00,D,,0,0,0,,{\\i1x", ASS)
	f.Fuzz(func(t *testing.T, text, format string) {
		cues, err := ParseCues(text, format)
		if err == nil {
			_ = WriteSRT(cues)
		}
	})
}

func TestASSText_StripsVectorDrawingsAndEmptyStyleTags(t *testing.T) {
	if got := ASSText(`{\p1}m 0 0 l 100 0 b 120 30{\p0}`); got != "" {
		t.Fatalf("pure drawing = %q, want empty", got)
	}
	if got := ASSText(`{\p1}m 0 0 l 100 0{\p0}After sign`); got != "After sign" {
		t.Fatalf("drawing then text = %q", got)
	}
	if got := ASSText(`{\pos(10,20)\an8}Positioned sign`); got != "Positioned sign" {
		t.Fatalf("pos tag confused with drawing: %q", got)
	}
	if got := ASSText(`{\i1\p1}m 0 0{\i0}`); got != "" {
		t.Fatalf("empty style around drawing = %q, want empty", got)
	}
	if got := ASSText(`{\p4}m 0 0\Nl 10 10{\i1}hidden{\p0}Visible`); got != "Visible" {
		t.Fatalf("scaled drawing = %q", got)
	}
}

func TestParseCues_ASSCoalescesMotionTrackedAndSkipsDrawings(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("[Script Info]\nScriptType: v4.00+\n\n[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n")
	// 25,000 motion-tracked 24fps sign frames (42ms each) plus vector drawing frames and layered duplicates.
	for i := 0; i < 25000; i++ {
		startMS := i * 42
		endMS := startMS + 42
		sb.WriteString("Dialogue: 0," + fmtASSClock(startMS) + "," + fmtASSClock(endMS) + ",Sign,,0,0,0,,{\\pos(100,200)}CHAPTER ONE\n")
	}
	// Layered blur/outline duplicate at the same time.
	sb.WriteString("Dialogue: 1,0:00:00.00,0:00:00.50,Sign,,0,0,0,,{\\blur2}CHAPTER ONE\n")
	// Vector drawing cue.
	sb.WriteString("Dialogue: 0,0:01:00.00,0:01:02.00,Draw,,0,0,0,,{\\p1}m 0 0 l 100 100{\\p0}\n")
	// Genuinely repeated dialogue separated by > 150ms.
	sb.WriteString("Dialogue: 0,0:20:00.00,0:20:01.00,Default,,0,0,0,,Wait!\n")
	sb.WriteString("Dialogue: 0,0:20:02.00,0:20:03.00,Default,,0,0,0,,Wait!\n")

	cues, err := ParseCues(sb.String(), ASS)
	if err != nil {
		t.Fatalf("ParseCues failed: %v", err)
	}
	if len(cues) != 3 {
		t.Fatalf("got %d cues, want 3: %+v", len(cues), cues)
	}
	if cues[0].Text != "CHAPTER ONE" || cues[0].StartMS != 0 || cues[0].EndMS != 25000*42 {
		t.Fatalf("coalesced cue = %+v", cues[0])
	}
	if cues[1].Text != "Wait!" || cues[2].Text != "Wait!" || cues[1].StartMS == cues[2].StartMS {
		t.Fatalf("repeated dialogue cues = %+v", cues[1:])
	}
}

func fmtASSClock(ms int) string {
	cs := (ms / 10) % 100
	s := (ms / 1000) % 60
	m := (ms / 60000) % 60
	h := ms / 3600000
	return strings.Join([]string{
		string(rune('0' + h)),
		":",
		twoDigits(m),
		":",
		twoDigits(s),
		".",
		twoDigits(cs),
	}, "")
}

func twoDigits(n int) string {
	return string([]byte{byte('0' + n/10), byte('0' + n%10)})
}
