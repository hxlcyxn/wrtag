//go:build unix

package wrtag

import (
	"os"
	"syscall"
)

func init() {
	albumGroupsMatch = func(parent, file os.FileInfo) bool {
		parentStat, parentOK := parent.Sys().(*syscall.Stat_t)
		fileStat, fileOK := file.Sys().(*syscall.Stat_t)
		return parentOK && fileOK && parentStat.Gid == fileStat.Gid
	}
}
