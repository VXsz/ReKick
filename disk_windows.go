//go:build windows
// +build windows

package main

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

var shutdownSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}

func checkDiskSpace(archiveDir string, requiredBytes int64) error {
	var freeBytesAvailable uint64

	pathPtr, err := windows.UTF16PtrFromString(archiveDir)
	if err != nil {
		return fmt.Errorf("failed to convert path for disk space check: %v", err)
	}

	err = windows.GetDiskFreeSpaceEx(pathPtr, &freeBytesAvailable, nil, nil)
	if err != nil {
		return fmt.Errorf("failed to check disk space: %v", err)
	}

	if freeBytesAvailable < uint64(requiredBytes) {
		return fmt.Errorf("insufficient disk space: need %s, have %s",
			formatBytes(requiredBytes), formatBytes(int64(freeBytesAvailable)))
	}

	logz("ok", EMOJI_CHECK, "Sufficient disk space available (%s required, %s available)",
		formatBytes(requiredBytes), formatBytes(int64(freeBytesAvailable)))
	return nil
}
