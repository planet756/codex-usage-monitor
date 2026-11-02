//go:build !linux && !windows

package rollout

import "os"

func stampFile(_ *os.File, info os.FileInfo) sourceStamp {
	return sourceStamp{Size: info.Size(), Modified: info.ModTime().UnixNano()}
}
