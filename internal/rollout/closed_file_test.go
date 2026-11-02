package rollout

import (
	"errors"
	"os"
	"testing"
)

// A repeated Close has a portable ErrClosed contract. Windows File.Stat may
// instead expose ERROR_INVALID_HANDLE, so it is not a portable closure oracle.
// If a handle was leaked, this assertion closes it but still fails the test.
func requireClosedFile(t testing.TB, file *os.File) {
	t.Helper()
	if err := file.Close(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("file was not already closed: %v", err)
	}
}
