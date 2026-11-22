package releasename

import (
	"slices"
	"testing"
)

// "Title.2016.E12.720p": an episode number with no season is the show's
// running number, not a movie.
func TestBareEpisodeNumber(t *testing.T) {
	for _, c := range []struct {
		name, title string
		year        int
		abs         []int
	}{
		{"Berserk.2016.E12.720p.WEB.x264-ANiURL", "Berserk", 2016, []int{12}},
		{"Berserk.2016.E07.WEB.x264-ANiURL-Obfuscated", "Berserk", 2016, []int{7}},
		{"Quiet Harbour E105 1080p WEB-GRP", "Quiet Harbour", 0, []int{105}},
		{"Quiet.Harbour.e03v2.1080p.WEB-GRP", "Quiet Harbour", 0, []int{3}},
	} {
		r := Parse(c.name, Hints{})
		if r.Title.Value != c.title || r.Year.Value != c.year || r.Kind.Value != KindAbsolute || r.Show == nil || !slices.Equal(r.Show.Absolute, c.abs) {
			t.Errorf("%s:\n title %q year %d kind %q show %+v", c.name, r.Title.Value, r.Year.Value, r.Kind.Value, r.Show)
		}
	}
}

// "E12.5" is a recap between two episodes, a single digit is too little to
// go on, and SxxEyy reads as before.
func TestBareEpisodeNumberLeftAlone(t *testing.T) {
	for _, name := range []string{
		"Berserk.2016.E12.5.1080p.WEB.x264-ANiURL",
		"Berserk.2016.E12.5.WEB.x264-ANiURL",
		"Plan.E5.2019.1080p.WEB-GRP",
	} {
		if r := Parse(name, Hints{}); r.Show != nil && len(r.Show.Absolute) > 0 {
			t.Errorf("%s: read as running number %v", name, r.Show.Absolute)
		}
	}
	r := Parse("Berserk.2016.S01E12.1080p.BluRay-iVy", Hints{})
	if r.Kind.Value != KindEpisode || r.Show == nil || !slices.Equal(r.Show.Episodes, []int{12}) {
		t.Errorf("SxxEyy: kind %q show %+v", r.Kind.Value, r.Show)
	}
}
