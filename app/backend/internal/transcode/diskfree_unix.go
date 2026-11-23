//go:build unix

package transcode

import (
	"fmt"
	"syscall"
)

func fsUsage(path string) (availBytes uint64, usedPct int, fsID string, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil || st.Blocks == 0 || st.Bsize <= 0 {
		return 0, 0, "", false
	}
	bsize := uint64(st.Bsize)
	availBytes = st.Bavail * bsize
	var usedBlocks uint64
	if st.Blocks > st.Bfree {
		usedBlocks = st.Blocks - st.Bfree
	}
	usedPct = int((usedBlocks*100 + st.Blocks/2) / st.Blocks)
	if usedPct < 0 {
		usedPct = 0
	}
	if usedPct > 100 {
		usedPct = 100
	}
	fsID = fmt.Sprintf("%v", st.Fsid)
	return availBytes, usedPct, fsID, true
}
