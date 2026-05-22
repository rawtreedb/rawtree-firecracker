//go:build !linux

package observability

import (
	"context"
	"fmt"
)

func LaunchObservedSandbox(ctx context.Context, request SandboxLaunchRequest) error {
	return fmt.Errorf("this reference starts real Firecracker, so it must run on a Linux host with KVM")
}

func AssertCanRunFirecracker() error {
	return fmt.Errorf("this reference starts real Firecracker, so it must run on a Linux host with KVM")
}
