package subtext

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func fits(t *testing.T, cues []Cue) {
	t.Helper()
	for i, c := range cues {
		if c.EndMS-c.StartMS > MaxLineMS {
			t.Fatalf("line %d lasts %d ms: %q", i, c.EndMS-c.StartMS, c.Text)
		}
		if n := utf8.RuneCountInString(strings.ReplaceAll(c.Text, "\n", " ")); n > MaxLineChars {
			t.Fatalf("line %d holds %d characters: %q", i, n, c.Text)
		}
		if c.EndMS <= c.StartMS || (i > 0 && c.StartMS < cues[i-1].EndMS) {
			t.Fatalf("line %d has odd times %d to %d", i, c.StartMS, c.EndMS)
		}
	}
}

func TestShapeSplitsALongLineAtASentenceEnd(t *testing.T) {
	in := []Cue{{StartMS: 1000, EndMS: 13000, Text: "We should leave before the rain starts. Nobody told me the bridge was closed."}}
	got := Shape(in)
	if len(got) != 2 || got[0].Text != "We should leave before the rain starts." || got[1].Text != "Nobody told me the bridge was closed." {
		t.Fatalf("got %+v", got)
	}
	if got[0].StartMS != 1000 || got[1].EndMS != 13000 || got[0].EndMS != got[1].StartMS {
		t.Fatalf("times %+v", got)
	}
	fits(t, got)
}

func TestShapeSplitsOverlongText(t *testing.T) {
	words := strings.TrimSpace(strings.Repeat("lantern ", 28)) // about 220 characters, no punctuation
	got := Shape([]Cue{{StartMS: 0, EndMS: 6000, Text: words}})
	if len(got) < 3 {
		t.Fatalf("%d parts", len(got))
	}
	fits(t, got)
	if strings.Join(strings.Fields(strings.Join(texts(got), " ")), " ") != words {
		t.Fatal("words were lost or reordered")
	}
}

func texts(cues []Cue) []string {
	out := make([]string, len(cues))
	for i, c := range cues {
		out[i] = c.Text
	}
	return out
}

func TestShapeKeepsShortLinesAsTheyAre(t *testing.T) {
	in := []Cue{{StartMS: 0, EndMS: 1500, Text: "Good morning."}, {StartMS: 2000, EndMS: 4000, Text: "<i>Is anyone there?</i>"}}
	got := Shape(in)
	if len(got) != 2 || got[0] != in[0] || got[1] != in[1] {
		t.Fatalf("got %+v", got)
	}
}

func TestShapeBreaksATwoRowLine(t *testing.T) {
	got := Shape([]Cue{{StartMS: 0, EndMS: 4000, Text: "I left the keys on the kitchen table before we went out this morning"}})
	if len(got) != 1 || strings.Count(got[0].Text, "\n") != 1 {
		t.Fatalf("got %+v", got)
	}
	for _, row := range strings.Split(got[0].Text, "\n") {
		if utf8.RuneCountInString(row) > MaxLineChars/2 {
			t.Fatalf("row too long: %q", row)
		}
	}
}

func TestShapeDropsRepeatsAndSoundOnlyLines(t *testing.T) {
	in := []Cue{
		{StartMS: 0, EndMS: 1000, Text: "Thank you."}, {StartMS: 1000, EndMS: 2000, Text: "Thank you."}, {StartMS: 2000, EndMS: 3000, Text: " thank you. "},
		{StartMS: 3000, EndMS: 4000, Text: "[Music]"}, {StartMS: 4000, EndMS: 5000, Text: "♪♪"}, {StartMS: 5000, EndMS: 6000, Text: "(applause)"},
		{StartMS: 6000, EndMS: 7000, Text: "..."}, {StartMS: 7000, EndMS: 8000, Text: "Where were we?"}, {StartMS: 9000, EndMS: 10000, Text: "Thank you."},
	}
	got := texts(Shape(in))
	want := []string{"Thank you.", "Where were we?", "Thank you."}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q", got)
	}
}

func TestShapeHandlesTextWithoutSpaces(t *testing.T) {
	run := strings.Repeat("あいうえお", 40) // 200 characters, no spaces
	got := Shape([]Cue{{StartMS: 0, EndMS: 6000, Text: run}})
	fits(t, got)
	if strings.Join(texts(got), "") != run {
		t.Fatal("characters were lost")
	}
}

func TestTooFew(t *testing.T) {
	// Lines spread over a twenty-minute file, one every four minutes.
	spread := func(n int) []Cue {
		out := make([]Cue, n)
		for i := range out {
			out[i] = Cue{StartMS: int64(i) * 240000, EndMS: int64(i)*240000 + 2000, Text: "x"}
		}
		return out
	}
	if !TooFew(spread(4), 20*60) || TooFew(spread(4), 5*60) || !TooFew(nil, 60) || TooFew(spread(5), 20*60) {
		t.Fatal("too-few rule")
	}
}

func TestShapedLinesRoundTripAsSRT(t *testing.T) {
	in := Shape([]Cue{{StartMS: 500, EndMS: 2500, Text: "First line."}, {StartMS: 3000, EndMS: 5000, Text: "Second\nline."}})
	back, err := ParseCues(WriteSRT(in), SRT)
	if err != nil || len(back) != len(in) {
		t.Fatalf("%v %+v", err, back)
	}
	for i := range in {
		if back[i] != in[i] {
			t.Fatalf("cue %d: %+v, want %+v", i, back[i], in[i])
		}
	}
}

// Lines for only the start of a long file are not a transcript of it: the
// transcriber stopped early, and saving that would look like the whole thing.
func TestTooFewWhenOnlyTheStartIsCovered(t *testing.T) {
	early := make([]Cue, 40)
	for i := range early {
		early[i] = Cue{StartMS: int64(i) * 7000, EndMS: int64(i)*7000 + 3000, Text: "x"}
	}
	if !TooFew(early, 7200) {
		t.Fatal("40 lines in the first five minutes of a two-hour file passed as a transcript")
	}
	spread := make([]Cue, 40)
	for i := range spread {
		spread[i] = Cue{StartMS: int64(i) * 170000, EndMS: int64(i)*170000 + 3000, Text: "x"}
	}
	if TooFew(spread, 7200) {
		t.Fatal("lines spread over the file were refused")
	}
	// A short file is not held to it: its speech may well all come early.
	if TooFew(early[:6], 500) {
		t.Fatal("a short file was refused")
	}
}
