//go:build !windows

package deploy

import (
	"os"
	"path/filepath"
	"syscall"
)

// dataDirOwner 返回数据目录的属主。macOS 上数据目录在用户的 ~/Library 下，
// 属主就是该用户；服务以该用户身份运行，提权写入的文件也要交还给他。
func dataDirOwner(dir string) (uid, gid int, ok bool) {
	fi, err := os.Stat(dir)
	if err != nil {
		return 0, 0, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return int(st.Uid), int(st.Gid), true
}

// fixOwner 以 root 运行时把 paths（递归）交还给数据目录的属主，避免之后以用户身份运行的
// 程序无法改写提权进程留下的文件。属主本身是 root 时不做任何事。
func fixOwner(dataDir string, paths ...string) {
	if os.Geteuid() != 0 {
		return
	}
	uid, gid, ok := dataDirOwner(dataDir)
	if !ok || uid == 0 {
		return
	}
	for _, p := range paths {
		_ = filepath.Walk(p, func(path string, _ os.FileInfo, err error) error {
			if err == nil {
				_ = os.Lchown(path, uid, gid)
			}
			return nil
		})
	}
}
