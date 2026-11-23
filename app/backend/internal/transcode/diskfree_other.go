//go:build !unix

package transcode

func fsUsage(_ string) (uint64, int, string, bool) {
	return 0, 0, "", false
}
