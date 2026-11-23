package main

import (
	"context"
	"strconv"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/jobs"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/parts"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles"
	"github.com/HolyArrs/holyarrs/app/backend/internal/transcode"
)

func newTranscode(ctx context.Context, s *database.Store, live transcode.Publisher, q *jobs.Queue, subs *subtitles.Service, pc *parts.Control, safeMode bool) *transcode.Service {
	tc := transcode.NewService(s, live, q)
	tc.YieldGPU = func(_ context.Context) {
		if subs != nil {
			subs.YieldGPU()
		}
	}
	if subs != nil {
		tc.GPUVRAMProbe = func(ctx context.Context) []transcode.GPUVRAMInfo {
			devs := subs.Devices(ctx)
			out := make([]transcode.GPUVRAMInfo, 0, len(devs))
			for i, d := range devs {
				out = append(out, transcode.GPUVRAMInfo{
					Index:    parseDeviceIndex(d.ID, i),
					Name:     d.Name,
					MemoryMB: d.MemoryMB,
					FreeMB:   d.FreeMB,
				})
			}
			return out
		}
		tc.YieldGPUVRAM = func(ctx context.Context, card int, reserveMB int) {
			subs.YieldGPUForVRAM(ctx, card, reserveMB)
		}
		subs.ExternalGPUVRAMMB = func(_ context.Context, card int) int {
			return tc.ActiveGPUVRAMMB(card)
		}
	}
	tc.OnTranscoded = func(ctx context.Context, fileID int64) {
		if subs != nil {
			_, _ = subs.Look(ctx, fileID, domain.System)
		}
	}
	tc.LoadPaused(ctx)
	q.SetGate(transcode.PauseGate(subtitles.PauseGate(pc, subs), tc))
	tc.Register(q)
	if !safeMode {
		s.OnEvent(func(e domain.Event) {
			if pc.On(parts.Movies) || pc.On(parts.Shows) {
				tc.OnEvent(e)
			}
		})
	}
	return tc
}

func parseDeviceIndex(id string, fallback int) int {
	i := len(id)
	for i > 0 && id[i-1] >= '0' && id[i-1] <= '9' {
		i--
	}
	if i == len(id) {
		return fallback
	}
	n, err := strconv.Atoi(id[i:])
	if err != nil {
		return fallback
	}
	return n
}
