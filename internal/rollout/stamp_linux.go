package rollout

import (
	"fmt"
	"os"
	"syscall"
)

func stampFile(_ *os.File, info os.FileInfo) sourceStamp {
	s := sourceStamp{Size: info.Size(), Modified: info.ModTime().UnixNano()}
	if v, ok := info.Sys().(*syscall.Stat_t); ok {
		s.Identity = fmt.Sprintf("%x:%x", v.Dev, v.Ino)
		s.Change = v.Ctim.Sec*1e9 + v.Ctim.Nsec
		s.Trusted = s.Change != 0 && v.Ino != 0
	}
	return s
}
