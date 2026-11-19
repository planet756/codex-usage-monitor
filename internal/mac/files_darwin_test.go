package mac

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestDesktopSocketRequiresPrivateDirectoryAndLocalSocket(t *testing.T) {
	// Darwin's sockaddr_un has a short path limit; the test name plus the
	// system's /var/folders temporary root can exceed it before dialing.
	home, err := os.MkdirTemp("/tmp", "cum-ipc-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	dir := filepath.Join(home, "ipc")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, "ipc.sock"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	c, err := dialIPC(home)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if err = os.Chmod(dir, 0777); err != nil {
		t.Fatal(err)
	}
	if c, err = dialIPC(home)(context.Background()); err == nil {
		c.Close()
		t.Fatal("accepted writable IPC directory")
	}
}
