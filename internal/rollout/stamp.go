package rollout

import "os"

// Change is the filesystem's metadata change counter/time, not merely mtime.
// Unsupported filesystems never qualify for metadata-only reuse.
type sourceStamp struct {
	Identity string
	Size     int64
	Modified int64
	Change   int64
	USN      uint64 `json:",omitempty"`
	Trusted  bool
}

func (s sourceStamp) unchanged(other sourceStamp) bool {
	return s.Trusted && other.Trusted && s == other
}

func stampPath(path string) (sourceStamp, error) {
	f, err := os.Open(path)
	if err != nil {
		return sourceStamp{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return sourceStamp{}, err
	}
	return stampFile(f, info), nil
}
