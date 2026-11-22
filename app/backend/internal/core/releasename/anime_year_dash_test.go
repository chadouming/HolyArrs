package releasename

import (
	"slices"
	"testing"
)

// Some groups put the show's year before the running number, dots for
// spaces: "[SRW].Hellsing.2001-13.[1080p.x265.AAC]" is episode 13 of the
// 2001 Hellsing.
func TestAnimeNumberRightAfterTheYear(t *testing.T) {
	for _, c := range []struct {
		name, title string
		year        int
		abs         []int
	}{
		{"[SRW].Hellsing.2001-13.[1080p.x265.AAC]", "Hellsing", 2001, []int{13}},
		{"[SRW].Hellsing.2001-01.[1080p.x265.AAC]", "Hellsing", 2001, []int{1}},
		{"[SRW] Hellsing 2001-13 [1080p x265 AAC]", "Hellsing", 2001, []int{13}},
		{"[SRW].Quiet.Harbour.2019-07v2.[1080p.x265.AAC]", "Quiet Harbour", 2019, []int{7}},
	} {
		r := Parse(c.name, Hints{})
		if r.Title.Value != c.title || r.Year.Value != c.year || r.Group.Value != "SRW" || r.Show == nil || !slices.Equal(r.Show.Absolute, c.abs) {
			t.Errorf("%s:\n title %q year %d group %q show %+v", c.name, r.Title.Value, r.Year.Value, r.Group.Value, r.Show)
		}
		if r.Resolution.Value != 1080 {
			t.Errorf("%s: resolution %d", c.name, r.Resolution.Value)
		}
	}
}

// Two years are a span of years and a full date is a date, not a year and an
// episode; a name with no [group] is left alone. ("2019-12" alone could be a
// month, but real groups write the episode that way, and a wrong reading
// still has to name an episode the show has.)
func TestYearDashThatIsNotAnEpisode(t *testing.T) {
	for _, name := range []string{
		"[Group] Some Show 2001-2004 [BD 1080p]",
		"[Group] Live Concert 2019-12-25 [BD]",
		"Some.Show.2001-13.1080p.WEB-GRP",
	} {
		if r := Parse(name, Hints{}); r.Show != nil && len(r.Show.Absolute) == 1 && r.Show.Absolute[0] < 100 {
			t.Errorf("%s: read as an episode: %+v", name, r.Show)
		}
	}
}
