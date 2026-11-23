package importer

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"strings"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/mediafiles"
)

func inspectShowSteps(
	ctx context.Context,
	db *sql.DB,
	plan []mediafiles.PlanStep,
	indices []int,
	slots map[int64]*showSlot,
	ver mediafiles.Version,
	lf mediafiles.LibraryFolder,
	pr mediafiles.PickRules,
	relName string,
	music bool,
	fakes fakeRules,
	mismatch string,
	olds map[int][]mediafiles.File,
	inspected map[string]map[string]any,
	confirmedSamples map[string]bool,
) bool {
	newSamples := false
	for _, i := range indices {
		if plan[i].Method != "hardlink" {
			continue
		}
		seen := map[int64]bool{}
		for _, id := range plan[i].TargetSlotIDs {
			ss := slots[id]
			if ss == nil || ss.sl.FileID == nil || seen[*ss.sl.FileID] {
				continue
			}
			seen[*ss.sl.FileID] = true
			var f mediafiles.File
			if err := database.Get(ctx, db, "files", *ss.sl.FileID, &f); err == nil && f.State == "present" {
				olds[i] = append(olds[i], f)
			}
		}
		copyWhy := ""
		for _, old := range olds[i] {
			if old.Protected {
				copyWhy = "the copy in place is protected; choose another Version for an upgrade"
			}
		}
		minutes := 0
		for _, id := range plan[i].TargetSlotIDs {
			if ss := slots[id]; ss != nil && ss.it.Runtime != nil && minutes >= 0 {
				minutes += *ss.it.Runtime
			} else {
				minutes = -1
			}
		}
		if !music && copyWhy == "" {
			facts := InspectFacts(plan[i].SourceFile)
			inspected[plan[i].SourceFile] = facts
			if sampleByLength(facts, fakes.lengthMinutes(max(minutes, 0))) {
				plan[i].Method, plan[i].TargetPath = "refuse", ""
				plan[i].Why = append(plan[i].Why, "a sample, not the full video (it plays "+lengthWords(factInt(facts, "runtime_seconds"))+")")
				delete(olds, i)
				if !confirmedSamples[plan[i].SourceFile] {
					confirmedSamples[plan[i].SourceFile] = true
					newSamples = true
				}
				continue
			}
			copyWhy = unreadOr(copyRefusal(ver, filepath.Base(plan[i].SourceFile), relName, "", facts), facts, plan[i].SourceFile)
		}
		if copyWhy != "" {
			plan[i].Method, plan[i].TargetPath = "refuse", ""
			plan[i].Why = append(plan[i].Why, copyWhy)
			delete(olds, i)
			continue
		}
		why, err := fakes.fakeFileOf(plan[i].SourceFile, music, max(minutes, 0), relName)
		if err != nil {
			plan[i].Method, plan[i].TargetPath = "refuse", ""
			plan[i].Why = append(plan[i].Why, unreadable(err))
			continue
		}
		if why == "" {
			why = mismatch
		}
		if why != "" {
			for _, id := range plan[i].TargetSlotIDs {
				slots[id].lie = why
			}
			plan[i].Method, plan[i].TargetPath = "refuse", ""
			plan[i].Why = append(plan[i].Why, why)
			continue
		}
		name := strings.TrimSuffix(filepath.Base(plan[i].SourceFile), filepath.Ext(plan[i].SourceFile))
		oldName := ""
		if len(olds[i]) > 0 {
			oldName = fileName(olds[i][0])
		}
		why = lieOf(name, relName, len(olds[i]) > 0, oldName, pr.Body, inspected[plan[i].SourceFile])
		if why == "" && music && len(olds[i]) > 0 {
			why = lossyOverLossless(plan[i].SourceFile, relName, olds[i][0], filepath.Join(lf.Path, filepath.FromSlash(olds[i][0].RelativePath)), pr.Body)
		}
		if why != "" {
			for _, id := range plan[i].TargetSlotIDs {
				slots[id].lie = why
			}
			plan[i].Method, plan[i].TargetPath = "refuse", ""
			plan[i].Why = append(plan[i].Why, why)
			delete(olds, i)
			continue
		}
		if len(olds[i]) > 0 {
			id := olds[i][0].ID
			plan[i].ReplacesFileID = &id
		}
	}
	return newSamples
}

func allPlanIndices(n int) []int {
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	return idx
}

func showSampleFallback(
	ctx context.Context,
	db *sql.DB,
	cands []Candidate,
	t ShowTarget,
	plan []mediafiles.PlanStep,
	slots map[int64]*showSlot,
	ver mediafiles.Version,
	lf mediafiles.LibraryFolder,
	pr mediafiles.PickRules,
	relName string,
	fakes fakeRules,
	mismatch string,
	olds map[int][]mediafiles.File,
	inspected map[string]map[string]any,
	confirmedSamples map[string]bool,
	recheckGuard func([]mediafiles.PlanStep),
) []mediafiles.PlanStep {
	for range len(cands) {
		filled := filledSlotSet(plan)
		if !hasUnfilledWanted(t.Episodes, filled) {
			break
		}
		remaining := excludingCandidates(cands, confirmedSamples, acceptedSources(plan))
		if !hasVideoCandidate(remaining) {
			break
		}
		tNext := t
		tNext.Episodes = slices.Clone(t.Episodes)
		for i := range tNext.Episodes {
			if filled[tNext.Episodes[i].SlotID] {
				tNext.Episodes[i].Wanted = false
			}
		}
		nextPlan := PlanEpisodes(remaining, tNext)
		var added []int
		plan, added = mergePromotedSteps(plan, nextPlan)
		if len(added) == 0 {
			break
		}
		if recheckGuard != nil {
			recheckGuard(plan)
		}
		if !inspectShowSteps(ctx, db, plan, added, slots, ver, lf, pr, relName, false, fakes, mismatch, olds, inspected, confirmedSamples) {
			break
		}
	}
	return plan
}

func mergePromotedSteps(plan, nextPlan []mediafiles.PlanStep) ([]mediafiles.PlanStep, []int) {
	var added []int
	for _, ns := range nextPlan {
		if ns.Method != "hardlink" {
			continue
		}
		replaced := false
		for i := range plan {
			if plan[i].SourceFile == ns.SourceFile && plan[i].Method == "skip" {
				plan[i] = ns
				added = append(added, i)
				replaced = true
				break
			}
		}
		if !replaced {
			plan = append(plan, ns)
			added = append(added, len(plan)-1)
		}
	}
	return plan, added
}

func (im *Importer) movieSampleFallback(
	ctx context.Context,
	db *sql.DB,
	sl mediafiles.Slot,
	ver mediafiles.Version,
	d domain.Download,
	t Target,
	libPath, root, unpackWhy string,
	bad []mediafiles.PlanStep,
	cands []Candidate,
	rules FileRules,
	fakes fakeRules,
	minutes int,
	plan []mediafiles.PlanStep,
	up upgrade,
	lowSpace *SpaceError,
	release func(),
) ([]mediafiles.PlanStep, upgrade, *SpaceError, func(), error) {
	confirmedSamples := map[string]bool{}
	var refusedSamples []mediafiles.PlanStep
	for range len(cands) {
		if hasMain(plan) || len(up.samples) == 0 {
			break
		}
		added := false
		for _, st := range plan {
			if up.samples[st.SourceFile] && !confirmedSamples[st.SourceFile] {
				confirmedSamples[st.SourceFile] = true
				refusedSamples = append(refusedSamples, st)
				added = true
			}
		}
		if !added {
			break
		}
		remaining := excludingCandidates(cands, confirmedSamples, nil)
		if !hasVideoCandidate(remaining) {
			break
		}
		nextPlan := append(slices.Clone(refusedSamples), Plan(remaining, t)...)
		nextPlan = append(nextPlan, bad...)
		if unpackWhy != "" {
			nextPlan = append(nextPlan, unpackStep(root, unpackWhy))
		}
		plan = nextPlan
		if release != nil {
			release()
		}
		lowSpace, release = im.guard(plan, libPath, cands, rules)
		var err error
		up, err = checkUpgrade(ctx, db, sl, ver, d, plan, cands, fakes, minutes)
		if err != nil {
			return plan, up, lowSpace, release, err
		}
	}
	return plan, up, lowSpace, release, nil
}

func filledSlotSet(plan []mediafiles.PlanStep) map[int64]bool {
	filled := map[int64]bool{}
	for _, st := range plan {
		if st.Method == "hardlink" {
			for _, id := range st.TargetSlotIDs {
				filled[id] = true
			}
		}
	}
	return filled
}

func hasUnfilledWanted(eps []Episode, filled map[int64]bool) bool {
	for _, e := range eps {
		if e.Wanted && !filled[e.SlotID] {
			return true
		}
	}
	return false
}

func acceptedSources(plan []mediafiles.PlanStep) map[string]bool {
	out := map[string]bool{}
	for _, st := range plan {
		if st.Method == "hardlink" {
			out[st.SourceFile] = true
		}
	}
	return out
}

func excludingCandidates(cands []Candidate, excluded, accepted map[string]bool) []Candidate {
	var out []Candidate
	for _, c := range cands {
		if excluded[c.Path] || accepted[c.Path] {
			continue
		}
		out = append(out, c)
	}
	return out
}

func hasVideoCandidate(cands []Candidate) bool {
	for _, c := range cands {
		if videoExt[strings.ToLower(filepath.Ext(c.Path))] {
			return true
		}
	}
	return false
}
