//go:build !windows

package rollout

import "os"

func temporaryIndex() (*os.File, error) {
	f, err := os.CreateTemp("", "cum-turns-*.idx")
	if err != nil {
		return nil, err
	}
	// Unlink immediately: the kernel also reclaims it after abnormal process exit.
	if err := os.Remove(f.Name()); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
