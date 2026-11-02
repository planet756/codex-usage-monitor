//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
)

func prepareConsole()   {}
func showError(e error) { fmt.Fprintln(os.Stderr, e) }
func platformRun(o options) error {
	return errors.New("the overlay and lifecycle watcher require Windows; --inspect works on Linux")
}
