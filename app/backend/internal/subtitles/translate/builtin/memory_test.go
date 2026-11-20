package builtin

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTotalAndFreeMemoryFromFiles(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "proc/meminfo"), "MemTotal:       16777216 kB\nMemFree: 1 kB\nMemAvailable:    8388608 kB\n")
	total, free := memoryAt(root)
	if total != 16<<30 || free != 8<<30 {
		t.Fatalf("no limit: total %d free %d", total, free)
	}
	// A container limit smaller than the host wins, less what is in use.
	write(t, filepath.Join(root, "proc/self/cgroup"), "0::/box\n")
	write(t, filepath.Join(root, "sys/fs/cgroup/box/memory.max"), "4294967296\n")
	write(t, filepath.Join(root, "sys/fs/cgroup/box/memory.current"), "1073741824\n")
	total, free = memoryAt(root)
	if total != 4<<30 || free != 3<<30 {
		t.Fatalf("limit: total %d free %d", total, free)
	}
	// No limit ("max") leaves the host's numbers.
	write(t, filepath.Join(root, "sys/fs/cgroup/box/memory.max"), "max\n")
	if total, free = memoryAt(root); total != 16<<30 || free != 8<<30 {
		t.Fatalf("max: total %d free %d", total, free)
	}
	// Nothing readable: unknown.
	if total, free = memoryAt(t.TempDir()); total != 0 || free != 0 {
		t.Fatalf("unknown: %d %d", total, free)
	}
}
