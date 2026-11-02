package rollout

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func temporaryIndex() (*os.File, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	name := filepath.Join(os.TempDir(), "cum-turns-"+hex.EncodeToString(nonce[:])+".idx")
	path, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(path, windows.GENERIC_READ|windows.GENERIC_WRITE|windows.DELETE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.CREATE_NEW,
		windows.FILE_ATTRIBUTE_TEMPORARY|windows.FILE_FLAG_DELETE_ON_CLOSE, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(h), name)
	if f == nil {
		windows.CloseHandle(h)
		return nil, os.ErrInvalid
	}
	return f, nil
}
