// Package proc starts helper programs (ffmpeg, ffprobe). On Windows it
// keeps them from opening a console window, which they would whenever
// the desktop app, having no console of its own, starts one.
package proc

import (
	"context"
	"os/exec"
)

// Command is exec.CommandContext for helper programs.
func Command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	hideWindow(cmd)
	return cmd
}
