package subtitles

import (
	"strconv"

	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate/builtin"
)

func deviceMemoryMB(devs []builtin.Device, id string) int {
	for _, d := range devs {
		if d.ID == id {
			return d.MemoryMB
		}
	}
	return 0
}

func cardMemoryMB(devs []builtin.Device, card int) int {
	for _, d := range devs {
		if m := deviceNumber.FindStringSubmatch(d.ID); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil && n == card {
				return d.MemoryMB
			}
		}
	}
	return 0
}

func speechCapForCard(memoryMB int, modelNeedBytes uint64) int {
	if memoryMB <= 0 {
		return MaxTranscribeAtOnce
	}
	switch {
	case memoryMB < 5000:
		return 1
	case memoryMB < 7500:
		if modelNeedBytes <= 2<<30 {
			return 2
		}
		return 1
	case memoryMB < 10000:
		return 2
	default:
		return MaxTranscribeAtOnce
	}
}

func effectiveTranscribeAtOnce(wanted int, cards []int, devs []builtin.Device, modelNeedBytes uint64) int {
	atOnce := min(max(wanted, 1), MaxTranscribeAtOnce)
	for _, card := range cards {
		if cap := speechCapForCard(cardMemoryMB(devs, card), modelNeedBytes); cap < atOnce {
			atOnce = cap
		}
	}
	return max(atOnce, 1)
}

func tightVRAM(memoryMB int) bool {
	return memoryMB > 0 && memoryMB <= 8500
}
