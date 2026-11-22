package importer

import (
	"strings"
	"testing"

	"github.com/HolyArrs/holyarrs/app/backend/internal/mediafiles"
)

func partShow() ShowTarget {
	ns := mediafiles.NamingDefaults()
	ns.RenameOnImport = true
	var eps []Episode
	abs := 0
	for s, n := range []int{3, 2} {
		for e := 1; e <= n; e++ {
			abs++
			eps = append(eps, Episode{Ref: mediafiles.EpisodeRef{ItemID: int64(abs), Season: s + 1, Episode: e, Absolute: abs}, Name: "Ep", SlotID: int64(100 + abs), Wanted: true})
		}
	}
	parts := []mediafiles.PartName{{Names: []string{"Copper Valley Ridge Line"}, Season: 2, Source: mediafiles.PartFromSeasonName}}
	return ShowTarget{LibraryPath: "/lib", FolderName: "Copper Valley", Show: mediafiles.TitleRef{Name: "Copper Valley", Kind: "show", Parts: parts},
		Order: mediafiles.OrderAired, Naming: &ns, Episodes: eps}
}

// A downloaded file named after one part of the show lands on that part's
// episode.
func TestPlanCountsInsideThePart(t *testing.T) {
	steps := PlanEpisodes([]Candidate{{Path: "/dl/x/[Group] Copper Valley Ridge Line - 02 [1080p].mkv", Size: 1}}, partShow())
	if steps[0].Method != "hardlink" || len(steps[0].TargetSlotIDs) != 1 || steps[0].TargetSlotIDs[0] != 105 || !strings.Contains(steps[0].TargetPath, "S02E02") {
		t.Fatalf("got %+v", steps[0])
	}
}
