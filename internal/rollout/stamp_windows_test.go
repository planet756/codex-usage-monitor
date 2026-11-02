package rollout

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"
)

func TestFileBasicInfoLayout(t *testing.T) {
	var info fileBasicInfo
	if unsafe.Sizeof(info) != 40 || unsafe.Offsetof(info.Change) != 24 || unsafe.Offsetof(info.Attributes) != 32 {
		t.Fatal("FILE_BASIC_INFO ABI mismatch")
	}
}

func TestWindowsStampRejectsSameSizeRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "same-size.jsonl")
	if err := os.WriteFile(path, []byte("before\n"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := stampPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("after!\n"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := stampPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if before.unchanged(after) {
		t.Fatalf("content rewrite reused a trusted stamp: before=%+v after=%+v", before, after)
	}
	if before.USN == 0 && before.Trusted || after.USN == 0 && after.Trusted {
		t.Fatal("journal-free source was trusted without a byte check")
	}
	t.Logf("journal witness available=%v", before.USN != 0 && after.USN != 0)
}
