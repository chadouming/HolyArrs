package transcode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadFFmpegProgressFieldsAndOutTimeFallback(t *testing.T) {
	input := strings.Join([]string{
		"frame=120",
		"fps=48.5",
		"total_size=524288",
		"out_time_us=N/A",
		"out_time_ms=N/A",
		"out_time=00:00:05.250000",
		"speed=2.0x",
		"progress=continue",
		"frame=240",
		"fps=50.0",
		"total_size=1048576",
		"out_time_us=-500",
		"out_time_ms=-500",
		"out_time=00:01:02.500000",
		"speed=2.1x",
		"progress=continue",
		"frame=300",
		"total_size=2097152",
		"out_time_us=65000000",
		"out_time=00:00:01.000000",
		"progress=end",
	}, "\n") + "\n"

	var updates []progressUpdate
	err := readFFmpegProgress(strings.NewReader(input), func(u progressUpdate) {
		updates = append(updates, u)
	})
	if err != nil {
		t.Fatalf("readFFmpegProgress failed: %v", err)
	}
	if len(updates) != 3 {
		t.Fatalf("expected 3 progress updates, got %d (%+v)", len(updates), updates)
	}

	if updates[0].Frame != 120 || updates[0].TotalSize != 524288 || updates[0].OutSeconds != 5.25 {
		t.Fatalf("unexpected update[0]: %+v", updates[0])
	}
	if updates[1].Frame != 240 || updates[1].TotalSize != 1048576 || updates[1].OutSeconds != 62.5 {
		t.Fatalf("unexpected update[1]: %+v", updates[1])
	}
	if updates[2].Frame != 300 || updates[2].TotalSize != 2097152 || updates[2].OutSeconds != 65.0 {
		t.Fatalf("unexpected update[2] (out_time_us should override out_time): %+v", updates[2])
	}
}

func TestRunStreamingFFmpegProgressWithNAOutTimeAndStallState(t *testing.T) {
	orig := ffmpegStallTimeout
	ffmpegStallTimeout = 200 * time.Millisecond
	t.Cleanup(func() {
		ffmpegStallTimeout = orig
	})

	dir := t.TempDir()

	// Advancing frame and total_size must keep watchdog alive even when out_time_us=N/A.
	advancingScript := filepath.Join(dir, "ffmpeg-advance")
	script1 := `#!/usr/bin/env bash
for i in 1 2 3 4 5 6; do
  printf 'frame=%d\ntotal_size=%d\nout_time_us=N/A\nprogress=continue\n' "$((i*20))" "$((i*10000))"
  sleep 0.06
done
printf 'frame=140\ntotal_size=70000\nout_time_us=N/A\nprogress=end\n'
`
	if err := os.WriteFile(advancingScript, []byte(script1), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := runStreamingFFmpeg(context.Background(), nil, advancingScript, []string{"-version"}, nil); err != nil {
		t.Fatalf("expected advancing frame/total_size with out_time_us=N/A not to stall, got: %v", err)
	}

	// Frozen frame/total_size/out_time must trip stall watchdog and include last_frame/last_out/last_size.
	stalledScript := filepath.Join(dir, "ffmpeg-stall")
	script2 := `#!/usr/bin/env bash
printf 'frame=42\ntotal_size=12345\nout_time_us=2500000\nprogress=continue\n'
while true; do
  printf 'frame=42\ntotal_size=12345\nout_time_us=2500000\nprogress=continue\n'
  sleep 0.05
done
`
	if err := os.WriteFile(stalledScript, []byte(script2), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := runStreamingFFmpeg(context.Background(), nil, stalledScript, []string{"-version"}, nil)
	if err == nil {
		t.Fatal("expected stalled error")
	}
	msg := err.Error()
	for _, want := range []string{"stalled", "last_frame=42", "last_out=2.5s", "last_size=12345"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("expected %q in stall error %q", want, msg)
		}
	}
}
