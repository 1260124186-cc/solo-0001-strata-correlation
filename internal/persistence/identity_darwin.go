//go:build darwin

package persistence

import (
	"os"
	"syscall"
)

func snapshotIdentityFromInfo(info os.FileInfo) snapshotIdentity {
	identity := snapshotIdentity{
		size:        info.Size(),
		modTimeNano: info.ModTime().UnixNano(),
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		identity.device = uint64(stat.Dev)
		identity.file = stat.Ino
	}
	return identity
}
