//go:build !windows
// +build !windows

package main

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

var shutdownSignals = []os.Signal{syscall.SIGINT, syscall.SIGTERM}

func checkDiskSpace(archiveDir string, requiredBytes int64) error {
	var stat unix.Statfs_t
	if err := unix.Statfs(archiveDir, &stat); err != nil {
		return fmt.Errorf("failed to check disk space: %v", err)
	}

	available := stat.Bavail * uint64(stat.Bsize)
	required := uint64(requiredBytes)

	if available < required {
		return fmt.Errorf("insufficient disk space: need %s, have %s",
			formatBytes(requiredBytes), formatBytes(int64(available)))
	}

	logz("ok", EMOJI_CHECK, "Sufficient disk space available (%s required, %s available)",
		formatBytes(requiredBytes), formatBytes(int64(available)))
	return nil
}
