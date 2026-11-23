package transcode

import (
	"fmt"
	"math"
	"strings"
)

const widescreenMinDAR = (16.0 / 9.0) - 0.02

// ScalePlan describes how input dimensions map onto normalized output geometry.
type ScalePlan struct {
	SourceW           int
	SourceH           int
	SARNum            int
	SARDen            int
	DAR               float64
	ScaledW           int
	ScaledH           int
	FrameW            int
	FrameH            int
	NeedsPad          bool
	PadX              int
	PadY              int
	AlreadyNormalized bool
}

func roundEven(v float64) int {
	n := int(math.Round(v/2.0)) * 2
	if n < 2 {
		return 2
	}
	return n
}

// ComputeScalePlan computes scaling and optional 4:3 pillarboxing geometry for cfg.
func ComputeScalePlan(width, height int, sarStr string, cfg ProfileConfig) (ScalePlan, error) {
	if width <= 0 || height <= 0 {
		return ScalePlan{}, fmt.Errorf("invalid video dimensions: %dx%d", width, height)
	}
	sarNum, sarDen := ParseRatio(sarStr, 1, 1)
	dar := (float64(width) * float64(sarNum)) / (float64(height) * float64(sarDen))

	scaleMode := strings.ToLower(strings.TrimSpace(cfg.ScaleMode))
	if scaleMode == "" {
		if cfg.Normalize1080p {
			scaleMode = "normalize"
		} else {
			scaleMode = "none"
		}
	}
	targetH := cfg.MaxHeight
	if targetH <= 0 {
		if scaleMode == "normalize" && cfg.Normalize1080p {
			targetH = 1080
		} else {
			scaleMode = "none"
		}
	}

	if scaleMode == "none" {
		sw, sh := width, height
		if sw%2 != 0 {
			sw = roundEven(float64(sw))
		}
		if sh%2 != 0 {
			sh = roundEven(float64(sh))
		}
		already := width == sw && height == sh && sarNum == sarDen
		return ScalePlan{
			SourceW:           width,
			SourceH:           height,
			SARNum:            sarNum,
			SARDen:            sarDen,
			DAR:               dar,
			ScaledW:           sw,
			ScaledH:           sh,
			FrameW:            sw,
			FrameH:            sh,
			AlreadyNormalized: already,
		}, nil
	}

	targetW := roundEven(float64(targetH) * 16.0 / 9.0)

	if scaleMode == "downscale_only" && width <= targetW && height <= targetH {
		if cfg.Pillarbox43 && dar >= 1.0 && dar < widescreenMinDAR {
			sw := roundEven(float64(width) * float64(sarNum) / float64(sarDen))
			sh := roundEven(float64(height))
			padTargetW := roundEven(float64(sh) * 16.0 / 9.0)
			if sw < padTargetW {
				return ScalePlan{
					SourceW:           width,
					SourceH:           height,
					SARNum:            sarNum,
					SARDen:            sarDen,
					DAR:               dar,
					ScaledW:           sw,
					ScaledH:           sh,
					FrameW:            padTargetW,
					FrameH:            sh,
					NeedsPad:          true,
					PadX:              (padTargetW - sw) / 2,
					PadY:              0,
					AlreadyNormalized: false,
				}, nil
			}
		}
		sw := roundEven(float64(width) * float64(sarNum) / float64(sarDen))
		sh := roundEven(float64(height))
		already := width == sw && height == sh && sarNum == sarDen
		return ScalePlan{
			SourceW:           width,
			SourceH:           height,
			SARNum:            sarNum,
			SARDen:            sarDen,
			DAR:               dar,
			ScaledW:           sw,
			ScaledH:           sh,
			FrameW:            sw,
			FrameH:            sh,
			AlreadyNormalized: already,
		}, nil
	}

	var scaledW, scaledH, frameW, frameH, padX, padY int
	var needsPad bool

	switch {
	case dar >= 1.0 && dar < widescreenMinDAR:
		scaledH = targetH
		scaledW = roundEven(float64(targetH) * dar)
		if scaledW > targetW {
			scaledW = targetW
		}
		if cfg.Pillarbox43 {
			frameW, frameH = targetW, targetH
			needsPad = scaledW < targetW
			padX = (targetW - scaledW) / 2
		} else {
			frameW, frameH = scaledW, scaledH
		}
	case dar >= widescreenMinDAR:
		scaledW = targetW
		scaledH = roundEven(float64(targetW) / dar)
		if scaledH > targetH {
			scaledH = targetH
		}
		frameW, frameH = scaledW, scaledH
	default:
		scaledH = targetH
		scaledW = roundEven(float64(targetH) * dar)
		frameW, frameH = scaledW, scaledH
	}

	already := width == scaledW &&
		height == scaledH &&
		!needsPad &&
		sarNum == sarDen &&
		dar >= widescreenMinDAR

	return ScalePlan{
		SourceW:           width,
		SourceH:           height,
		SARNum:            sarNum,
		SARDen:            sarDen,
		DAR:               dar,
		ScaledW:           scaledW,
		ScaledH:           scaledH,
		FrameW:            frameW,
		FrameH:            frameH,
		NeedsPad:          needsPad,
		PadX:              padX,
		PadY:              padY,
		AlreadyNormalized: already,
	}, nil
}

// ResolveColorHandling decides whether to tonemap HDR to SDR (bt709) or preserve source color tags.
func ResolveColorHandling(v ProbeStream, hdrMode string) (bool, []string) {
	prim := strings.ToLower(strings.TrimSpace(v.ColorPrimaries))
	trc := strings.ToLower(strings.TrimSpace(v.ColorTransfer))
	sp := strings.ToLower(strings.TrimSpace(v.ColorSpace))
	cr := strings.ToLower(strings.TrimSpace(v.ColorRange))

	hdrKind := DetectSourceHDR(v)
	isDV5 := hdrKind == "dv5"
	isHDR := hdrKind != ""
	wantTonemap := isDV5 || (strings.EqualFold(strings.TrimSpace(hdrMode), "tonemap_sdr") && isHDR)

	if wantTonemap {
		return true, []string{
			"-color_primaries", "bt709",
			"-color_trc", "bt709",
			"-colorspace", "bt709",
			"-color_range", "tv",
		}
	}

	var flags []string
	if prim != "" && prim != "unknown" {
		flags = append(flags, "-color_primaries", prim)
	}
	if trc != "" && trc != "unknown" {
		flags = append(flags, "-color_trc", trc)
	}
	if sp != "" && sp != "unknown" {
		flags = append(flags, "-colorspace", sp)
	}
	if cr != "" && cr != "unknown" {
		flags = append(flags, "-color_range", cr)
	}
	return false, flags
}

// BuildVideoFilter builds the ffmpeg -vf string for scaling, pillarboxing, tonemapping, and hwupload.
func BuildVideoFilter(plan ScalePlan, pixFmt string, tonemapToSDR, hwuploadVAAPI, hwuploadVulkan, useTonemapx bool) string {
	var steps []string
	if !plan.AlreadyNormalized {
		steps = append(steps, fmt.Sprintf("scale=%d:%d:flags=bicubic", plan.ScaledW, plan.ScaledH))
		if plan.NeedsPad {
			steps = append(steps, fmt.Sprintf("pad=%d:%d:%d:%d:black", plan.FrameW, plan.FrameH, plan.PadX, plan.PadY))
		}
		steps = append(steps, "setsar=1")
	}

	if tonemapToSDR {
		if useTonemapx {
			steps = append(steps,
				"tonemapx=tonemap=hable:desat=0.2:peak=100:t=bt709:m=bt709:p=bt709:r=tv",
				"setparams=color_primaries=bt709:color_trc=bt709:colorspace=bt709:range=tv",
			)
		} else {
			steps = append(steps,
				"zscale=t=linear:npl=1000",
				"tonemap=tonemap=hable:desat=0.2",
				"zscale=p=bt709:t=bt709:m=bt709:r=tv",
				"setparams=color_primaries=bt709:color_trc=bt709:colorspace=bt709:range=tv",
			)
		}
	}

	steps = append(steps, "format="+pixFmt)
	if hwuploadVAAPI || hwuploadVulkan {
		steps = append(steps, "hwupload")
	}
	return strings.Join(steps, ",")
}
