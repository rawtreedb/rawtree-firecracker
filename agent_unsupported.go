//go:build !linux

package main

import "fmt"

func runGuestAgent(port uint32) error {
	return fmt.Errorf("the guest vsock agent must run on Linux")
}
