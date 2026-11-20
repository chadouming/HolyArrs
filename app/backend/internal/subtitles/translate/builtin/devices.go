package builtin

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Device is a graphics card the runner can put a model on.
type Device struct {
	ID       string `json:"id"` // what Options.GPU takes, such as Vulkan0
	Name     string `json:"name"`
	MemoryMB int    `json:"memory_mb"`
	FreeMB   int    `json:"free_mb"`
}

// deviceLine is one line of the runner's device list:
// "  Vulkan0: Intel(R) Arc(tm) B580 Graphics (BMG G21) (12216 MiB, 10552 MiB free)".
var deviceLine = regexp.MustCompile(`^\s*([A-Za-z]+[0-9]+):\s+(.+?)\s+\((\d+) MiB, (\d+) MiB free\)\s*$`)

// DeviceID reports whether id has the form of a device id.
func DeviceID(id string) bool {
	return len(id) <= 32 && regexp.MustCompile(`^[A-Za-z]+[0-9]+$`).MatchString(id)
}

// Devices asks the runner which graphics cards it can use. None is not an
// error: the container was given no card, or the image has no driver for it.
func Devices(ctx context.Context, binary string) ([]Device, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "--list-devices")
	cmd.SysProcAttr = childAttr()
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("the model runner could not list graphics cards: %w", err)
	}
	devs := []Device{}
	for _, line := range strings.Split(string(out), "\n") {
		m := deviceLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		mem, _ := strconv.Atoi(m[3])
		free, _ := strconv.Atoi(m[4])
		devs = append(devs, Device{ID: m[1], Name: m[2], MemoryMB: mem, FreeMB: free})
	}
	return devs, nil
}
