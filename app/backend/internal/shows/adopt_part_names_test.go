package shows

import (
	"testing"

	"github.com/HolyArrs/holyarrs/app/backend/internal/library"
	"github.com/HolyArrs/holyarrs/app/backend/internal/mediafiles"
	"github.com/HolyArrs/holyarrs/app/backend/internal/metadata"
)

// A file in the show's folder named after one part of the show is that
// part's episode, on a rescan (the stored part names) as well as on add.
func TestMapEpisodeFilesCountsInsideThePart(t *testing.T) {
	sh := metadata.Show{Name: "JoJo's Bizarre Adventure", OtherNames: []string{"JoJo no Kimyou na Bouken: Ougon no Kaze"}}
	abs := 0
	for s, n := range []int{26, 48, 39, 39} {
		for e := 1; e <= n; e++ {
			abs++
			sh.Episodes = append(sh.Episodes, metadata.Episode{Season: s + 1, Number: e, Absolute: abs})
		}
	}
	vids := []library.AdoptVideo{{Path: "[Moozzi2] JoJo no Kimyou na Bouken Ougon no Kaze-20 [BD 1920x1080 x265-10Bit 2Audio].mkv", Size: 1 << 30}}
	parts := []mediafiles.PartName{{Names: []string{"JoJo no Kimyou na Bouken: Ougon no Kaze"}, Season: 4, Source: mediafiles.PartFromAnimeMap}}
	got := mapEpisodeFiles(vids, sh, "aired", parts)
	if len(got) != 1 || len(got[0].Keys) != 1 || got[0].Keys[0] != [2]int{4, 20} {
		t.Fatalf("got %+v", got)
	}
}
