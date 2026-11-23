package transcode

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// GPUVRAMInfo holds queried memory capacity and free headroom for one GPU index.
type GPUVRAMInfo struct {
	Index    int
	Name     string
	MemoryMB int
	FreeMB   int
}

// GPUDevice represents one DRM render node visible on the host.
type GPUDevice struct {
	Path     string `json:"path"`
	Vendor   string `json:"vendor"`
	Name     string `json:"name"`
	PCISlot  string `json:"pci_slot"`
	Discrete bool   `json:"discrete"`
	Selected bool   `json:"selected"`
	BusyJobs int    `json:"busy_jobs"`
	MemoryMB int    `json:"memory_mb,omitempty"`
	FreeMB   int    `json:"free_mb,omitempty"`
}

var renderNodeRe = regexp.MustCompile(`^renderD(\d+)$`)

func renderNodeNumber(name string) int {
	m := renderNodeRe.FindStringSubmatch(name)
	if m == nil {
		return 999999
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

func isIntelArcDeviceID(devID int64) bool {
	return (devID >= 0xE200 && devID <= 0xE2FF) || (devID >= 0x5690 && devID <= 0x56FF)
}

func vendorLabel(vendorHex string) string {
	switch strings.ToLower(strings.TrimSpace(vendorHex)) {
	case "0x8086":
		return "intel"
	case "0x1002":
		return "amd"
	case "0x10de":
		return "nvidia"
	default:
		return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(vendorHex)), "0x")
	}
}

func gpuDisplayName(vendor, pciSlot, nodeName string, discrete bool) string {
	switch vendor {
	case "intel":
		if discrete {
			return "Intel Arc GPU (" + nodeName + ")"
		}
		return "Intel Integrated GPU (" + nodeName + ")"
	case "amd":
		return "AMD GPU (" + nodeName + ")"
	case "nvidia":
		return "NVIDIA GPU (" + nodeName + ")"
	default:
		if pciSlot != "" {
			return nodeName + " (" + pciSlot + ")"
		}
		return nodeName
	}
}

func isReadableDevice(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

func readSysfsUint64(path string) (uint64, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func readSysfsVRAM(sysDev string) (memMB, freeMB int) {
	if totalBytes, ok := readSysfsUint64(filepath.Join(sysDev, "mem_info_vram_total")); ok && totalBytes > 0 {
		memMB = int(totalBytes / (1024 * 1024))
		freeMB = memMB
		if usedBytes, uOK := readSysfsUint64(filepath.Join(sysDev, "mem_info_vram_used")); uOK {
			if totalBytes > usedBytes {
				freeMB = int((totalBytes - usedBytes) / (1024 * 1024))
			} else {
				freeMB = 0
			}
		}
		return memMB, freeMB
	}
	if lmemBytes, ok := readSysfsUint64(filepath.Join(sysDev, "lmem_total_bytes")); ok && lmemBytes > 0 {
		memMB = int(lmemBytes / (1024 * 1024))
		return memMB, memMB
	}
	return 0, 0
}

func enrichGPUsWithVRAM(gpus []GPUDevice, probed []GPUVRAMInfo) []GPUDevice {
	out := append([]GPUDevice(nil), gpus...)
	var valid []GPUVRAMInfo
	for _, p := range probed {
		if p.MemoryMB > 0 {
			valid = append(valid, p)
		}
	}
	if len(valid) == 0 {
		return out
	}
	sort.SliceStable(valid, func(i, j int) bool {
		return valid[i].Index < valid[j].Index
	})

	var discreteMissing, allMissing []int
	for i := range out {
		if out[i].MemoryMB > 0 {
			continue
		}
		allMissing = append(allMissing, i)
		if out[i].Discrete {
			discreteMissing = append(discreteMissing, i)
		}
	}
	targets := discreteMissing
	if len(targets) == 0 {
		targets = allMissing
	}
	for k := 0; k < len(targets) && k < len(valid); k++ {
		idx := targets[k]
		p := valid[k]
		out[idx].MemoryMB = p.MemoryMB
		if p.FreeMB > 0 {
			out[idx].FreeMB = p.FreeMB
		} else if p.FreeMB == 0 {
			out[idx].FreeMB = 0
		}
	}
	return out
}

func gpuCardIndex(gpus []GPUDevice, path string) int {
	ord := 0
	for _, g := range gpus {
		if !g.Selected {
			continue
		}
		if g.Path == path {
			return ord
		}
		ord++
	}
	for i, g := range gpus {
		if g.Path == path {
			return i
		}
	}
	return 0
}

// DiscoverGPUs enumerates all readable render nodes and marks which ones are active.
func DiscoverGPUs(devDRI, sysfsDRM string, selectedGPUs []string, spec string, busyFn func(string) int) []GPUDevice {
	if devDRI == "" {
		devDRI = "/dev/dri"
	}
	if sysfsDRM == "" {
		sysfsDRM = "/sys/class/drm"
	}
	entries, err := os.ReadDir(devDRI)
	if err != nil {
		return []GPUDevice{}
	}
	var nodes []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "renderD") {
			nodes = append(nodes, e.Name())
		}
	}
	sort.Slice(nodes, func(i, j int) bool {
		ni, nj := renderNodeNumber(nodes[i]), renderNodeNumber(nodes[j])
		if ni != nj {
			return ni < nj
		}
		return nodes[i] < nodes[j]
	})

	devices := make([]GPUDevice, 0, len(nodes))
	anyDiscreteIntel := false
	for _, node := range nodes {
		devPath := filepath.Join(devDRI, node)
		if !isReadableDevice(devPath) {
			continue
		}
		dev := GPUDevice{
			Path: devPath,
			Name: node,
		}
		if busyFn != nil {
			dev.BusyJobs = busyFn(devPath)
		}
		sysDev := filepath.Join(sysfsDRM, node, "device")
		if vendorBytes, err := os.ReadFile(filepath.Join(sysDev, "vendor")); err == nil {
			vRaw := strings.TrimSpace(strings.ToLower(string(vendorBytes)))
			dev.Vendor = vendorLabel(vRaw)
			var devID int64
			if devBytes, err := os.ReadFile(filepath.Join(sysDev, "device")); err == nil {
				dRaw := strings.TrimPrefix(strings.TrimSpace(strings.ToLower(string(devBytes))), "0x")
				devID, _ = strconv.ParseInt(dRaw, 16, 64)
			}
			if resolved, err := filepath.EvalSymlinks(sysDev); err == nil {
				dev.PCISlot = strings.ToLower(filepath.Base(resolved))
			}
			if dev.Vendor == "intel" {
				isArc := isIntelArcDeviceID(devID)
				nonIGPU := dev.PCISlot != "" && dev.PCISlot != "0000:00:02.0"
				dev.Discrete = isArc || nonIGPU
				if dev.Discrete {
					anyDiscreteIntel = true
				}
			} else if dev.PCISlot != "" && dev.PCISlot != "0000:00:02.0" {
				dev.Discrete = true
			} else if dev.Vendor == "amd" || dev.Vendor == "nvidia" {
				dev.Discrete = true
			}
		}
		dev.Name = gpuDisplayName(dev.Vendor, dev.PCISlot, node, dev.Discrete)
		dev.MemoryMB, dev.FreeMB = readSysfsVRAM(sysDev)
		devices = append(devices, dev)
	}

	explicit := cleanStringSlice(selectedGPUs)
	if len(explicit) == 0 {
		cleanedSpec := strings.TrimSpace(spec)
		if cleanedSpec != "" && !strings.EqualFold(cleanedSpec, "auto") {
			for _, part := range strings.Split(cleanedSpec, ",") {
				if p := strings.TrimSpace(part); p != "" {
					explicit = append(explicit, p)
				}
			}
		}
	}

	for i := range devices {
		if len(explicit) > 0 {
			devices[i].Selected = slices.Contains(explicit, devices[i].Path)
		} else if anyDiscreteIntel {
			devices[i].Selected = devices[i].Vendor == "intel" && devices[i].Discrete
		} else {
			devices[i].Selected = true
		}
	}
	return devices
}

type gpuAllocator struct {
	mu      sync.Mutex
	busy    map[string]int
	rrIndex int
}

func (a *gpuAllocator) busyCount(path string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.busy[path]
}

func gpuFreeRank(g GPUDevice) int {
	if g.FreeMB > 0 {
		return g.FreeMB
	}
	return g.MemoryMB
}

// Acquire picks the least-busy selected GPU (breaking ties by highest free VRAM) and returns a release callback.
func (a *gpuAllocator) Acquire(gpus []GPUDevice) (GPUDevice, func(), bool) {
	var candidates []GPUDevice
	for _, g := range gpus {
		if g.Selected {
			candidates = append(candidates, g)
		}
	}
	if len(candidates) == 0 {
		return GPUDevice{}, func() {}, false
	}

	a.mu.Lock()
	if a.busy == nil {
		a.busy = map[string]int{}
	}
	n := len(candidates)
	start := a.rrIndex % n
	bestIdx := start
	bestLoad := a.busy[candidates[start].Path]
	bestFree := gpuFreeRank(candidates[start])
	for offset := 1; offset < n; offset++ {
		idx := (start + offset) % n
		load := a.busy[candidates[idx].Path]
		freeRank := gpuFreeRank(candidates[idx])
		if load < bestLoad || (load == bestLoad && freeRank > bestFree) {
			bestLoad = load
			bestFree = freeRank
			bestIdx = idx
		}
	}
	chosen := candidates[bestIdx]
	a.busy[chosen.Path]++
	chosen.BusyJobs = a.busy[chosen.Path]
	a.rrIndex = (bestIdx + 1) % n
	a.mu.Unlock()

	var once sync.Once
	release := func() {
		once.Do(func() {
			a.mu.Lock()
			if a.busy[chosen.Path] > 0 {
				a.busy[chosen.Path]--
			}
			a.mu.Unlock()
		})
	}
	return chosen, release, true
}
