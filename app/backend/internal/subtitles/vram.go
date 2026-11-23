package subtitles

import (
	"context"
	"fmt"
	"strconv"

	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate/builtin"
)

func deviceMemoryMB(devs []builtin.Device, id string) int {
	for _, d := range devs {
		if d.ID == id {
			return d.MemoryMB
		}
	}
	if m := deviceNumber.FindStringSubmatch(id); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return cardMemoryMB(devs, n)
		}
	}
	return 0
}

func cardDeviceID(devs []builtin.Device, card int) string {
	for _, d := range devs {
		if m := deviceNumber.FindStringSubmatch(d.ID); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil && n == card {
				return d.ID
			}
		}
	}
	return fmt.Sprintf("Vulkan%d", card)
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

func usableVRAMMB(totalMB int) int {
	if totalMB <= 0 {
		return 0
	}
	headroom := max(1024, totalMB/10)
	return max(0, totalMB-headroom)
}

func speechSlotVRAMMB(modelNeedBytes uint64) int {
	return max(2000, int(modelNeedBytes>>20)+500)
}

func translatorEstVRAMMB(modelNeedBytes uint64, parallel int) int {
	base := max(2000, int(modelNeedBytes>>20)+256)
	slotMB := 160
	if modelNeedBytes >= 4<<30 {
		slotMB = 280
	}
	return base + max(parallel, 1)*slotMB
}

func speechCapForAvailable(availMB int, modelNeedBytes uint64) int {
	if availMB <= 0 {
		return DefaultTranscribeAtOnce
	}
	per := speechSlotVRAMMB(modelNeedBytes)
	return min(max(availMB/per, 1), MaxTranscribeAtOnce)
}

func speechCapForCard(totalMB int, modelNeedBytes uint64) int {
	return speechCapForAvailable(usableVRAMMB(totalMB), modelNeedBytes)
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

func needsVRAMRoom(totalMB, needMB, otherMB int) bool {
	if totalMB <= 0 {
		return false
	}
	return tightVRAM(totalMB) || needMB+otherMB > usableVRAMMB(totalMB)
}

func (s *Service) externalVRAMMB(ctx context.Context, card int) int {
	if s == nil || s.ExternalGPUVRAMMB == nil || card < 0 {
		return 0
	}
	if v := s.ExternalGPUVRAMMB(ctx, card); v > 0 {
		return v
	}
	return 0
}

func (s *Service) activeSpeechNeedBytes(ctx context.Context) uint64 {
	if s != nil {
		if m, ok := s.speechModel(ctx); ok && m.NeedBytes > 0 {
			return m.NeedBytes
		}
	}
	return 2500 << 20
}

func (s *Service) activeTranslatorEstVRAMMB(ctx context.Context) int {
	if s != nil && s.Store != nil {
		t := readTranslator(ctx, s.Store.Read())
		if t.BuiltInModel != "" {
			if m, ok := builtin.ByID(t.BuiltInModel); ok && m.NeedBytes > 0 {
				return max(2400, int(m.NeedBytes>>20))
			}
		}
	}
	return 3072
}
