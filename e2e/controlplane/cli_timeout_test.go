//go:build e2e

package controlplane

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Not parallel: overrides the CLI binary environment variable.
func TestRunEntireTimeoutAllowsCleanup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture requires a POSIX shell")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "entire")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nif [ \"$1\" = cleanup ]; then echo cleaned; exit 0; fi\nexec sleep 2\n"), 0o700))
	t.Setenv("E2E_ENTIRE_BIN", bin)

	cleaned := false
	t.Run("timeout", func(t *testing.T) {
		// Cleanup must get its own deadline after the command's has expired.
		t.Cleanup(func() {
			stdout, _, err := runEntireWithTimeout(t, dir, time.Second, "cleanup")
			require.NoError(t, err)
			require.Equal(t, "cleaned\n", stdout)
			cleaned = true
		})
		_, _, err := runEntireWithTimeout(t, dir, 50*time.Millisecond)
		require.ErrorIs(t, err, context.DeadlineExceeded)
	})
	require.True(t, cleaned)
}
