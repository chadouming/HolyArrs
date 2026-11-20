package subtext

import (
	"regexp"
	"strings"
	"unicode"
)

// Limits of one line of a transcribed subtitle.
const (
	// MaxLineMS is the longest a line stays on screen.
	MaxLineMS = 7000
	// MaxLineChars is the most a line holds: two rows of 42.
	MaxLineChars = 84
	// fewLines and fewAfterSeconds say when a transcript is too thin to be one.
	fewLines        = 5
	fewAfterSeconds = 600
)

var (
	bracketed = regexp.MustCompile(`\[[^\]]*\]|\([^)]*\)|（[^）]*）|【[^】]*】`)
	// sentenceEnd, clauseEnd: where a line may be split, best first.
	sentenceEnd = "." + "?" + "!" + "…" + "。" + "？" + "！"
	clauseEnd   = "," + ";" + ":" + "，" + "、" + "；" + "："
)

// soundOnly reports a line that holds no speech: only a sound in brackets, a
// music mark or punctuation.
func soundOnly(text string) bool {
	rest := bracketed.ReplaceAllString(text, "")
	return !strings.ContainsFunc(rest, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) })
}

// Shape makes transcribed lines fit a screen: a line longer than MaxLineMS or
// MaxLineChars is split at a sentence end, else at a comma, else at a space,
// else between characters, and its time is shared out by the length of each
// part. Lines repeated back to back and lines with no speech are dropped.
func Shape(cues []Cue) []Cue {
	out := make([]Cue, 0, len(cues))
	last := ""
	for _, c := range cues {
		text := strings.TrimSpace(c.Text)
		if text == "" || soundOnly(text) {
			continue
		}
		key := strings.ToLower(strings.Join(strings.Fields(text), " "))
		if key == last {
			continue // said once, written twice: a transcriber's stutter
		}
		last = key
		c.Text = text
		out = split(out, c)
	}
	return out
}

func tooLong(c Cue) bool {
	return c.EndMS-c.StartMS > MaxLineMS || len([]rune(strings.ReplaceAll(c.Text, "\n", " "))) > MaxLineChars
}

// split appends c to out, cut into parts that fit.
func split(out []Cue, c Cue) []Cue {
	if !tooLong(c) {
		c.Text = rows(c.Text)
		return append(out, c)
	}
	r := []rune(strings.Join(strings.Fields(c.Text), " "))
	at := cutPoint(r)
	if at <= 0 || at >= len(r) {
		c.Text = rows(string(r))
		return append(out, c) // one character: nothing left to cut
	}
	a, b := strings.TrimSpace(string(r[:at])), strings.TrimSpace(string(r[at:]))
	mid := c.StartMS + (c.EndMS-c.StartMS)*int64(at)/int64(len(r))
	out = split(out, Cue{StartMS: c.StartMS, EndMS: mid, Text: a})
	return split(out, Cue{StartMS: mid, EndMS: c.EndMS, Text: b})
}

// cutPoint is where r is best cut in two: after the sentence end nearest its
// middle, else after such a comma, else at such a space, else at the middle.
func cutPoint(r []rune) int {
	for _, set := range []string{sentenceEnd, clauseEnd, " "} {
		best := -1
		for i, ch := range r {
			if !strings.ContainsRune(set, ch) || i == 0 || i >= len(r)-1 {
				continue
			}
			at := i + 1
			if set != " " {
				// "3.5" and "e.g" are not ends: an end is followed by a space,
				// or stands in text written without spaces.
				if r[i+1] != ' ' && ch < 0x3000 && ch != '…' {
					continue
				}
			} else {
				at = i
			}
			if best < 0 || abs(at-len(r)/2) < abs(best-len(r)/2) {
				best = at
			}
		}
		if best > 0 {
			return best
		}
	}
	return len(r) / 2
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// rows breaks a line that is too wide for one row at the space nearest its
// middle. A line that already has its own break, or no space, is left alone.
func rows(text string) string {
	r := []rune(text)
	if len(r) <= MaxLineChars/2 || strings.Contains(text, "\n") {
		return text
	}
	best := -1
	for i, ch := range r {
		if ch == ' ' && (best < 0 || abs(i-len(r)/2) < abs(best-len(r)/2)) {
			best = i
		}
	}
	if best <= 0 {
		return text
	}
	return string(r[:best]) + "\n" + string(r[best+1:])
}

// TooFew reports a transcript that is not one: no lines at all, or, for a
// file longer than ten minutes, fewer than five lines or lines that stop
// before half of the file, which is a transcriber that gave up early and not
// a video that goes quiet.
func TooFew(cues []Cue, fileSeconds float64) bool {
	if len(cues) == 0 {
		return true
	}
	if fileSeconds <= fewAfterSeconds {
		return false
	}
	last := cues[0].EndMS
	for _, c := range cues {
		last = max(last, c.EndMS)
	}
	return len(cues) < fewLines || float64(last) < fileSeconds*1000/2
}
