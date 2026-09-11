//go:build !linux && !darwin

package persistence

import "os"

func snapshotIdentityFromInfo(info os.FileInfo) snapshotIdentity {
	return snapshotIdentity{
		size:        info.Size(),
		modTimeNano: info.ModTime().UnixNano(),
	}
}
