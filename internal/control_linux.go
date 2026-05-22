//go:build linux

package observability

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"time"

	firecracker "github.com/firecracker-microvm/firecracker-go-sdk"
	fcvsock "github.com/firecracker-microvm/firecracker-go-sdk/vsock"
	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
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
	if len(options.Request.Argv) == 0 {
		return 127, fmt.Errorf("missing command")
	}

	collector := NewCollector(requestFromState(state, options.RawTree))
	execID := uuid.NewString()
	startedAt := time.Now()
	if !options.SuppressTelemetry {
		if err := collector.Record(Event{
			"event_type":  "sandbox.exec.started",
			"source":      "sandbox_vsock_control",
			"status":      "started",
			"exec_id":     execID,
			"command":     options.Request.Argv,
			"env":         options.Request.Env,
			"interactive": options.Request.Interactive,
			"sudo":        options.Request.Sudo,
			"workdir":     options.Request.Workdir,
		}); err != nil {
			return 1, err
		}
	}

	conn, err := fcvsock.DialContext(
		ctx,
		state.VsockPath,
		state.VsockPort,
		fcvsock.WithRetryTimeout(45*time.Second),
		fcvsock.WithLogger(log.New()),
	)
	if err != nil {
		if !options.SuppressTelemetry {
			_ = recordExecFailed(collector, execID, startedAt, err)
		}
		return 1, err
	}
	defer conn.Close()

	encoder := json.NewEncoder(conn)
	decoder := json.NewDecoder(conn)

	if err := encoder.Encode(options.Request); err != nil {
		if !options.SuppressTelemetry {
			_ = recordExecFailed(collector, execID, startedAt, err)
		}
		return 1, err
	}

	if options.Request.Interactive && options.Stdin != nil {
		go copyInteractiveInput(options.Stdin, encoder)
	}

	for {
		frame := ExecFrame{}
		if err := decoder.Decode(&frame); err != nil {
			if !options.SuppressTelemetry {
				_ = recordExecFailed(collector, execID, startedAt, err)
			}
			return 1, err
		}

		switch frame.Type {
		case "started":
			if !options.SuppressTelemetry {
				_ = collector.Record(Event{
					"event_type": "sandbox.exec.process.started",
					"source":     "sandbox_vsock_control",
					"status":     "success",
					"exec_id":    execID,
					"guest_pid":  frame.PID,
				})
			}
		case "stdout", "stderr":
			payload, err := base64.StdEncoding.DecodeString(frame.Data)
			if err != nil {
				continue
			}
			if frame.Type == "stdout" && options.Stdout != nil {
				_, _ = options.Stdout.Write(payload)
			}
			if frame.Type == "stderr" && options.Stderr != nil {
				_, _ = options.Stderr.Write(payload)
			}
			if !options.SuppressTelemetry {
				_ = collector.Record(Event{
					"event_type":      "sandbox.exec.output",
					"source":          "sandbox_vsock_control",
					"status":          "success",
					"exec_id":         execID,
					"stream":          frame.Type,
					"chunk_bytes":     len(payload),
					"chunk_preview":   truncate(string(payload), 500),
					"chunk_truncated": len(payload) > 500,
				})
			}
		case "exit":
			durationMS := time.Since(startedAt).Milliseconds()
			status := "success"
			if frame.ExitCode != 0 {
				status = "error"
			}
			if options.SuppressTelemetry {
				return frame.ExitCode, nil
			}
			err := collector.Record(Event{
				"event_type":  "sandbox.exec.completed",
				"source":      "sandbox_vsock_control",
				"status":      status,
				"exec_id":     execID,
				"exit_code":   frame.ExitCode,
				"duration_ms": durationMS,
			})
			return frame.ExitCode, err
		case "error":
			err := fmt.Errorf("%s", frame.Message)
			if !options.SuppressTelemetry {
				_ = recordExecFailed(collector, execID, startedAt, err)
			}
			if frame.ExitCode != 0 {
				return frame.ExitCode, err
			}
			return 1, err
		}
	}
}

func StopSandbox(ctx context.Context, state SandboxState, rawtree RawTreeConfig) error {
	collector := NewCollector(requestFromState(state, rawtree))
	if err := collector.Record(Event{
		"event_type": "sandbox.stop.requested",
		"source":     "sandbox_control_plane",
		"status":     "started",
	}); err != nil {
		return err
	}

	client := firecracker.NewClient(state.APISocketPath, log.NewEntry(log.New()), false)
	flushCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	_ = flushMetrics(flushCtx, client)
	cancel()
	if state.WorkspaceDir != "" {
		_ = os.WriteFile(providerStopMarkerPath(RuntimePaths{WorkspaceDir: state.WorkspaceDir}), []byte(time.Now().UTC().Format(time.RFC3339Nano)+"\n"), 0o644)
	}

	if state.SupervisorPID > 0 {
		if err := syscall.Kill(state.SupervisorPID, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
			_ = recordStopFailed(collector, err)
			return err
		}
	} else if state.FirecrackerPID > 0 {
		if err := syscall.Kill(state.FirecrackerPID, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
			_ = recordStopFailed(collector, err)
			return err
		}
	}

	return collector.Record(Event{
		"event_type": "sandbox.stop.sent",
		"source":     "sandbox_control_plane",
		"status":     "success",
	})
}

func WaitForSandboxStopped(ctx context.Context, sandboxID string, interval time.Duration) (SandboxState, error) {
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		state, err := ReadState(sandboxID)
		if err == nil && state.Status == "stopped" && !processIsRunning(state.SupervisorPID) {
			return state, nil
		}

		select {
		case <-ctx.Done():
			if err != nil {
				return SandboxState{}, err
			}
			return state, ctx.Err()
		case <-ticker.C:
		}
	}
}

func processIsRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	if stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		fields := strings.Fields(string(stat))
		if len(fields) > 2 && fields[2] == "Z" {
			return false
		}
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

func requestFromState(state SandboxState, rawtree RawTreeConfig) SandboxLaunchRequest {
	if rawtree.BaseURL == "" {
		rawtree.BaseURL = state.BaseURL
	}
	if rawtree.Table == "" {
		rawtree.Table = state.Table
	}

	return SandboxLaunchRequest{
		Metadata:  state.Metadata,
		Provider:  state.Provider,
		RawTree:   rawtree,
		RunID:     state.RunID,
		SandboxID: state.SandboxID,
	}
}

func copyInteractiveInput(reader io.Reader, encoder *json.Encoder) {
	buffer := make([]byte, 16*1024)
	for {
		n, err := reader.Read(buffer)
		if n > 0 {
			_ = encoder.Encode(ExecFrame{
				Type: "stdin",
				Data: base64.StdEncoding.EncodeToString(buffer[:n]),
			})
		}
		if err != nil {
			_ = encoder.Encode(ExecFrame{Type: "stdin_eof"})
			return
		}
	}
}

func recordExecFailed(collector *Collector, execID string, startedAt time.Time, err error) error {
	event := Event{
		"event_type":  "sandbox.exec.failed",
		"source":      "sandbox_vsock_control",
		"status":      "error",
		"exec_id":     execID,
		"duration_ms": time.Since(startedAt).Milliseconds(),
	}
	for key, value := range ErrorFields(err) {
		event[key] = value
	}
	return collector.Record(event)
}

func recordStopFailed(collector *Collector, err error) error {
	event := Event{
		"event_type": "sandbox.stop.failed",
		"source":     "sandbox_control_plane",
		"status":     "error",
	}
	for key, value := range ErrorFields(err) {
		event[key] = value
	}
	return collector.Record(event)
}
