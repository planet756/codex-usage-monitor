//go:build !windows

package storage

import "os"

func Replace(from, to string) error { return os.Rename(from, to) }
