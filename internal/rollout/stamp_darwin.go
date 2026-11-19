package rollout

import (
	"fmt"
	"os"
	"syscall"
)

func stampFile(f *os.File, info os.FileInfo) sourceStamp {
	s := sourceStamp{Size: info.Size(), Modified: info.ModTime().UnixNano()}
	if v, ok := info.Sys().(*syscall.Stat_t); ok {
		s.Identity = fmt.Sprintf("%x:%x", v.Dev, v.Ino)
		s.Change = v.Ctimespec.Sec*1e9 + v.Ctimespec.Nsec
		var fs syscall.Statfs_t
		// HFS and network mounts can have coarse change timestamps. Only a
		// local APFS file qualifies for metadata-only cache reuse; other mounts
		// retain byte-proof verification and the explicit file identity.
		if syscall.Fstatfs(int(f.Fd()), &fs) == nil {
			var name []byte
			for _, c := range fs.Fstypename {
				if c == 0 {
					break
				}
				name = append(name, byte(c))
			}
			s.Trusted = string(name) == "apfs" && fs.Flags&0x1000 != 0 && s.Change != 0 && v.Ino != 0
		}
	}
	return s
}
