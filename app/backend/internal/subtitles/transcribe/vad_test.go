package transcribe

import (
	"context"
	"strings"
	"testing"
)

// A film that opens with music: voice detection keeps a half-second blip at
// the start and then the speech, three minutes in. Whisper sees them side by
// side and gives one segment that starts at the blip. The words are placed
// where they are spoken, after the music, not at the start of the film.
func TestWordsAfterAnOpeningWithoutSpeechAreNotShownAtTheStart(t *testing.T) {
	o := setup(t, "en,en,en", "eng")
	t.Setenv("FAKE_LONG", "straddle") // the segment runs from 10 s to 410 s
	// Kept: 10.0-10.5 s (the blip), then 196-240 s. On the words' clock the
	// blip is 0-0.5 s and the speech starts at 0.6 s.
	t.Setenv("FAKE_VAD", "10.00,10.50,0.00,0.50;196.00,240.00,0.60,44.60")
	t.Setenv("FAKE_TOKEN_BASE", "1000") // the first word, 0.4 s into the speech
	res, err := Run(context.Background(), "video.mkv", Track{Seconds: 1200}, "en", o)
	if err != nil || len(res.Cues) < 4 {
		t.Fatalf("%d cues %v", len(res.Cues), err)
	}
	if res.Cues[0].StartMS != 196400 {
		t.Fatalf("the first line starts at %d ms, want 196400", res.Cues[0].StartMS)
	}
	var words []string
	for i, c := range res.Cues {
		if c.StartMS < 196000 || c.EndMS > 240000 || c.EndMS-c.StartMS > MaxLineMS || (i > 0 && c.StartMS < res.Cues[i-1].EndMS) {
			t.Fatalf("cue %d runs %d to %d", i, c.StartMS, c.EndMS)
		}
		words = append(words, strings.Fields(c.Text)...)
	}
	if len(words) != 12 {
		t.Fatalf("words %v", words)
	}
}

// Speech on both sides of a stretch without any: each word is placed on its
// own side, and no line runs across the stretch.
func TestWordsOnBothSidesOfASilenceKeepTheirOwnTimes(t *testing.T) {
	o := setup(t, "en,en,en", "eng")
	t.Setenv("FAKE_LONG", "straddle")
	// Kept: 10-22 s (six words of 2 s), then 300-312 s (the other six).
	t.Setenv("FAKE_VAD", "10.00,22.00,0.00,12.00;300.00,312.00,12.10,24.10")
	t.Setenv("FAKE_TOKEN_BASE", "0")
	res, err := Run(context.Background(), "video.mkv", Track{Seconds: 1200}, "en", o)
	if err != nil {
		t.Fatal(err)
	}
	var early, late int
	for i, c := range res.Cues {
		switch {
		case c.StartMS >= 10000 && c.EndMS <= 22500:
			early += len(strings.Fields(c.Text))
		case c.StartMS >= 299500 && c.EndMS <= 312500:
			late += len(strings.Fields(c.Text))
		default:
			t.Fatalf("cue %d runs %d to %d: across the silence", i, c.StartMS, c.EndMS)
		}
	}
	if early != 6 || late != 6 {
		t.Fatalf("%d words before the silence and %d after, want 6 and 6: %+v", early, late, res.Cues)
	}
}

func TestTimeMapPlacesAClockTime(t *testing.T) {
	m := timeMap{{vadStart: 0, vadEnd: 500, origStart: 10000, origEnd: 10500}, {vadStart: 600, vadEnd: 44600, origStart: 196000, origEnd: 240000}}
	for c, want := range map[int64]int64{0: 10000, 250: 10250, 500: 10500, 550: 196000, 600: 196000, 1000: 196400, 44600: 240000, 50000: 240000} {
		if got := m.orig(c); got != want {
			t.Errorf("%d ms on the words' clock is %d, want %d", c, got, want)
		}
	}
	if got := (timeMap{}).orig(1234); got != 1234 {
		t.Errorf("with nothing cut out a time stays itself, got %d", got)
	}
}
