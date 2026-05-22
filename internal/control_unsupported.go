//go:build !linux

package observability

import (
	"context"
	"fmt"
	"io"
	"time"
)

type ExecOptions struct {
	RawTree           RawTreeConfig
	Request           ExecRequest
	Stdin             io.Reader
	Stdout            io.Writer
	Stderr            io.Writer
	SuppressTelemetry bool
}

func ExecInSandbox(ctx context.Context, state SandboxState, options ExecOptions) (int, error) {
	return 1, fmt.Errorf("sandbox exec requires a Linux host with Firecracker vsock")
}

func StopSandbox(ctx context.Context, state SandboxState, rawtree RawTreeConfig) error {
	return fmt.Errorf("sandbox stop requires a Linux host with Firecracker")
}

func WaitForSandboxStopped(ctx context.Context, sandboxID string, interval time.Duration) (SandboxState, error) {
	return SandboxState{}, fmt.Errorf("sandbox stop waiting requires a Linux host with Firecracker")
}
