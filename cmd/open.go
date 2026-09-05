package cmd

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
)

// openInBrowser launches the OS's default handler for a file or URL.
// Non-blocking (Start, not Run) so the caller's process doesn't wait on
// the browser. Returns the launch error; the browser process itself may
// fail silently after that, which is acceptable for a convenience flag.
func openInBrowser(ctx context.Context, path string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "open", path)
	case "windows":
		cmd = exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", path)
	default:
		cmd = exec.CommandContext(ctx, "xdg-open", path)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	return nil
}
