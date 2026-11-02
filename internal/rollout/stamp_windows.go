package rollout

import (
	"encoding/binary"
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

type fileBasicInfo struct {
	Creation, Access, Write, Change int64
	Attributes                      uint32
	Padding                         uint32
}

const fsctlReadFileUSNData = 0x000900eb

// A Windows change time is metadata, not a content revision. Use the file's
// journal sequence only when the filesystem supplies one; otherwise callers
// verify cached bytes by hashing the consumed prefix.
func fileUSN(f *os.File) uint64 {
	versions := [2]uint16{2, 3}
	var output [128]uint64 // Eight-byte alignment for USN_RECORD_V2/V3.
	var returned uint32
	err := windows.DeviceIoControl(windows.Handle(f.Fd()), fsctlReadFileUSNData,
		(*byte)(unsafe.Pointer(&versions[0])), uint32(unsafe.Sizeof(versions)),
		(*byte)(unsafe.Pointer(&output[0])), uint32(unsafe.Sizeof(output)), &returned, nil)
	runtime.KeepAlive(f)
	if err != nil || returned < 32 || returned > uint32(unsafe.Sizeof(output)) {
		return 0
	}
	b := unsafe.Slice((*byte)(unsafe.Pointer(&output[0])), int(returned))
	length := binary.LittleEndian.Uint32(b[:4])
	var offset uint32
	switch binary.LittleEndian.Uint16(b[4:6]) {
	case 2:
		offset = 24
	case 3:
		offset = 40
	default:
		return 0
	}
	if length < offset+8 || length > returned {
		return 0
	}
	return binary.LittleEndian.Uint64(b[offset : offset+8])
}

func stampFile(f *os.File, info os.FileInfo) sourceStamp {
	s := sourceStamp{Size: info.Size(), Modified: info.ModTime().UnixNano()}
	var identity windows.ByHandleFileInformation
	var basic fileBasicInfo
	h := windows.Handle(f.Fd())
	if windows.GetFileInformationByHandle(h, &identity) == nil && windows.GetFileInformationByHandleEx(h, windows.FileBasicInfo, (*byte)(unsafe.Pointer(&basic)), uint32(unsafe.Sizeof(basic))) == nil {
		s.Identity = fmt.Sprintf("%x:%x:%x", identity.VolumeSerialNumber, identity.FileIndexHigh, identity.FileIndexLow)
		s.Change = basic.Change
		s.USN = fileUSN(f)
		s.Trusted = s.USN != 0 && (identity.FileIndexHigh != 0 || identity.FileIndexLow != 0)
	}
	return s
}
