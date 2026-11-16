//go:build !windows

package state

import "os"

func replaceStateFile(source, destination string) error { return os.Rename(source, destination) }
