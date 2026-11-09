//go:build !windows

package systs

import "syscall"

// freeMB 返回 path 所在分区的剩余空间，失败时为 0（视为未知）。
func freeMB(path string) int {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0
	}
	return int(uint64(st.Bavail) * uint64(st.Bsize) >> 20)
}
