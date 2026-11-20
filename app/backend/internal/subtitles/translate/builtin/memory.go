package builtin

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// TotalMemory is the memory this machine gives HolyArrs: the container's
// limit when one is set, otherwise the host's total. 0 when not known.
func TotalMemory() uint64 {
	total, _ := memoryAt("/")
	return total
}

// FreeMemory is the memory free right now, within the container's limit when
// one is set. 0 when not known.
func FreeMemory() uint64 {
	_, free := memoryAt("/")
	return free
}

// memoryAt reads total and free memory from the proc and cgroup files under root.
func memoryAt(root string) (total, free uint64) {
	raw, err := os.ReadFile(filepath.Join(root, "proc", "meminfo"))
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		kb, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			continue
		}
		switch f[0] {
		case "MemTotal:":
			total = kb << 10
		case "MemAvailable:":
			free = kb << 10
		}
	}
	limit, used := cgroupMemory(root)
	if limit > 0 && (total == 0 || limit < total) {
		total = limit
		room := uint64(0)
		if limit > used {
			room = limit - used
		}
		if free == 0 || room < free {
			free = room
		}
	}
	return total, free
}

// cgroupMemory is the smallest memory limit on this process's cgroup path
// and its parents, with the memory in use at that level; 0, 0 for no limit.
func cgroupMemory(root string) (limit, used uint64) {
	raw, err := os.ReadFile(filepath.Join(root, "proc", "self", "cgroup"))
	if err != nil {
		return 0, 0
	}
	num := func(p string) uint64 {
		b, err := os.ReadFile(p)
		if err != nil {
			return 0
		}
		n, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
		if err != nil || n >= 1<<62 {
			return 0 // "max", or a number that means no limit
		}
		return n
	}
	walk := func(base, rel, limitFile, usedFile string) {
		rel = filepath.Clean("/" + rel)
		for {
			dir := filepath.Join(base, rel)
			if n := num(filepath.Join(dir, limitFile)); n > 0 && (limit == 0 || n < limit) {
				limit, used = n, num(filepath.Join(dir, usedFile))
			}
			if rel == "/" || rel == "." {
				return
			}
			rel = filepath.Dir(rel)
		}
	}
	for _, line := range strings.Split(string(raw), "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), ":", 3)
		if len(parts) != 3 {
			continue
		}
		switch {
		case parts[0] == "0" && parts[1] == "":
			walk(filepath.Join(root, "sys", "fs", "cgroup"), parts[2], "memory.max", "memory.current")
		case strings.Contains(","+parts[1]+",", ",memory,"):
			walk(filepath.Join(root, "sys", "fs", "cgroup", "memory"), parts[2], "memory.limit_in_bytes", "memory.usage_in_bytes")
		}
	}
	return limit, used
}
