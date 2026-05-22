//go:build linux

package observability

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	firecracker "github.com/firecracker-microvm/firecracker-go-sdk"
	models "github.com/firecracker-microvm/firecracker-go-sdk/client/models"
	log "github.com/sirupsen/logrus"
)

const (
	DefaultBootArgs = "console=ttyS0 root=/dev/vda rw reboot=k panic=1 pci=off"
)

func LaunchObservedSandbox(ctx context.Context, request SandboxLaunchRequest) error {
	paths, err := runtimePaths(request.SandboxID)
	if err != nil {
		return err
	}

	var (
		cgroupDir      string
		machine        *firecracker.Machine
		metricsStopper func()
		sampler        *HypervisorSampler
	)

	collector := NewCollector(request)
	defer func() {
		if metricsStopper != nil {
			metricsStopper()
		}
		if sampler != nil {
			sampler.Stop()
		}
		if machine != nil {
			_ = machine.StopVMM()
		}
		if cgroupDir != "" {
			_ = os.Remove(cgroupDir)
		}
		_ = os.RemoveAll(paths.WorkspaceDir)
	}()

	if err := copyFile(request.RootFS, paths.RootFSCopyPath); err != nil {
		return err
	}

	if err := collector.Record(Event{
		"event_type": "sandbox.firecracker.provider.create.started",
		"source":     "firecracker_host_collector",
		"status":     "started",
	}); err != nil {
		return err
	}

	moveCgroup := cgroupMoveHandler(request.CgroupPath, &cgroupDir)
	machine, err = newMachine(ctx, request, paths, moveCgroup)
	if err != nil {
		return recordCreateFailure(collector, err)
	}

	if err := machine.Start(ctx); err != nil {
		return recordCreateFailure(collector, err)
	}

	pid, err := machine.PID()
	if err != nil {
		return recordCreateFailure(collector, err)
	}

	sampler = StartHypervisorSampler(
		collector,
		pid,
		time.Duration(request.HypervisorSampleIntervalMS)*time.Millisecond,
	)

	client := firecracker.NewClient(paths.APISocketPath, log.NewEntry(log.New()), false)
	metricsStopper = startMetricsFlusher(ctx, client, time.Duration(request.MetricsFlushIntervalMS)*time.Millisecond)

	if err := collector.Record(Event{
		"event_type":               "sandbox.firecracker.provider.vm.started",
		"source":                   "firecracker_host_collector",
		"status":                   "success",
		"api_socket_path":          paths.APISocketPath,
		"boot_args":                bootArgs(request.Firecracker),
		"firecracker_log_path":     paths.FirecrackerLogPath,
		"firecracker_metrics_path": paths.FirecrackerMetricsPath,
		"firecracker_pid":          pid,
		"sandbox_cgroup_path":      nullIfEmpty(request.CgroupPath),
		"sandbox_cgroup_host_path": nullIfEmpty(cgroupDir),
		"workspace_dir":            paths.WorkspaceDir,
		"firecracker_sdk_language": "go",
		"firecracker_sdk_module":   "github.com/firecracker-microvm/firecracker-go-sdk",
		"firecracker_sdk_migrated": true,
	}); err != nil {
		return err
	}

	fmt.Println("Firecracker sandbox started")
	fmt.Printf("Sandbox id: %s\n", request.SandboxID)
	fmt.Printf("Run id: %s\n", request.RunID)
	fmt.Printf("Firecracker log path: %s\n", paths.FirecrackerLogPath)
	fmt.Printf("Firecracker metrics path: %s\n", paths.FirecrackerMetricsPath)
	fmt.Printf("Workspace: %s\n", paths.WorkspaceDir)

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- machine.Wait(ctx)
	}()

	stopReason := "run_timeout_reached"
	exitCode := 0

	select {
	case err := <-waitCh:
		stopReason = "firecracker_exit"
		if err != nil {
			exitCode = 1
		}
	case <-time.After(time.Duration(request.RunTimeoutMS) * time.Millisecond):
		if metricsStopper != nil {
			metricsStopper()
			metricsStopper = nil
		}
		if sampler != nil {
			sampler.Sample()
		}
		_ = flushMetrics(ctx, client)
		if err := machine.StopVMM(); err != nil {
			exitCode = 1
		}
		select {
		case err := <-waitCh:
			if err != nil {
				exitCode = 1
			}
		case <-time.After(5 * time.Second):
			exitCode = 1
		}
	}

	if metricsStopper != nil {
		metricsStopper()
		metricsStopper = nil
	}
	if sampler != nil {
		sampler.Stop()
		sampler = nil
	}

	if err := EmitFirecrackerNativeEvents(paths, collector); err != nil {
		return err
	}

	status := "success"
	if stopReason == "firecracker_exit" && exitCode != 0 {
		status = "error"
	}

	if err := collector.Record(Event{
		"event_type":  "sandbox.firecracker.provider.vm.stopped",
		"source":      "firecracker_host_collector",
		"status":      status,
		"exit_code":   exitCode,
		"stop_reason": stopReason,
	}); err != nil {
		return err
	}

	return collector.Flush()
}

func AssertCanRunFirecracker() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("this reference starts real Firecracker, so it must run on a Linux host with KVM")
	}
	return nil
}

func newMachine(
	ctx context.Context,
	request SandboxLaunchRequest,
	paths RuntimePaths,
	moveCgroup firecracker.Opt,
) (*firecracker.Machine, error) {
	machineCfg := models.MachineConfiguration{
		MemSizeMib:      firecracker.Int64(request.Firecracker.MemMiB),
		Smt:             firecracker.Bool(false),
		TrackDirtyPages: false,
		VcpuCount:       firecracker.Int64(request.Firecracker.VCPUCount),
	}

	cfg := firecracker.Config{
		SocketPath:      paths.APISocketPath,
		LogPath:         paths.FirecrackerLogPath,
		LogLevel:        "Info",
		MetricsPath:     paths.FirecrackerMetricsPath,
		KernelImagePath: request.Firecracker.Kernel,
		KernelArgs:      bootArgs(request.Firecracker),
		Drives: firecracker.NewDrivesBuilder(paths.RootFSCopyPath).
			WithRootDrive(paths.RootFSCopyPath, firecracker.WithDriveID("rootfs")).
			Build(),
		MachineCfg: machineCfg,
		VMID:       request.SandboxID,
	}

	if request.Firecracker.Tap != "" {
		cfg.NetworkInterfaces = firecracker.NetworkInterfaces{
			{
				StaticConfiguration: &firecracker.StaticNetworkConfiguration{
					HostDevName: request.Firecracker.Tap,
					MacAddress:  guestMAC(request.Firecracker),
				},
			},
		}
	}

	logger := log.New()
	logger.SetOutput(io.Discard)

	cmd := firecracker.VMCommandBuilder{}.
		WithBin(request.Firecracker.Binary).
		WithSocketPath(paths.APISocketPath).
		WithStdout(io.Discard).
		WithStderr(os.Stderr).
		Build(ctx)

	return firecracker.NewMachine(
		ctx,
		cfg,
		firecracker.WithProcessRunner(cmd),
		firecracker.WithLogger(log.NewEntry(logger)),
		moveCgroup,
	)
}

func cgroupMoveHandler(cgroupPath string, cgroupDir *string) firecracker.Opt {
	return func(machine *firecracker.Machine) {
		if cgroupPath == "" {
			return
		}

		handler := firecracker.Handler{
			Name: "rawtree.MoveProcessToCgroup",
			Fn: func(ctx context.Context, machine *firecracker.Machine) error {
				pid, err := machine.PID()
				if err != nil {
					return err
				}

				dir, err := moveProcessToCgroup(pid, cgroupPath)
				if err != nil {
					return err
				}
				*cgroupDir = dir
				return nil
			},
		}

		machine.Handlers.FcInit = machine.Handlers.FcInit.AppendAfter(firecracker.StartVMMHandlerName, handler)
	}
}

func startMetricsFlusher(ctx context.Context, client *firecracker.Client, interval time.Duration) func() {
	if interval <= 0 {
		return nil
	}

	stopCh := make(chan struct{})
	doneCh := make(chan struct{})

	go func() {
		defer close(doneCh)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				if err := flushMetrics(ctx, client); err != nil {
					fmt.Fprintf(os.Stderr, "Firecracker metrics flush failed: %v\n", err)
				}
			}
		}
	}()

	return func() {
		close(stopCh)
		<-doneCh
	}
}

func flushMetrics(ctx context.Context, client *firecracker.Client) error {
	action := models.InstanceActionInfoActionTypeFlushMetrics
	_, err := client.CreateSyncAction(ctx, &models.InstanceActionInfo{
		ActionType: &action,
	})
	return err
}

func runtimePaths(sandboxID string) (RuntimePaths, error) {
	workspaceDir, err := os.MkdirTemp("", "rawtree-"+sandboxID+"-")
	if err != nil {
		return RuntimePaths{}, err
	}

	return RuntimePaths{
		APISocketPath:          filepath.Join(workspaceDir, "firecracker.socket"),
		FirecrackerLogPath:     filepath.Join(workspaceDir, "firecracker.log"),
		FirecrackerMetricsPath: filepath.Join(workspaceDir, "firecracker.metrics.jsonl"),
		RootFSCopyPath:         filepath.Join(workspaceDir, "rootfs.ext4"),
		WorkspaceDir:           workspaceDir,
	}, nil
}

func copyFile(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(destination)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func moveProcessToCgroup(pid int, cgroupPath string) (string, error) {
	relativePath, err := normalizeCgroupPath(cgroupPath)
	if err != nil {
		return "", err
	}

	cgroupDir := filepath.Join("/sys/fs/cgroup", relativePath)
	if err := os.MkdirAll(cgroupDir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(cgroupDir, "cgroup.procs"), []byte(fmt.Sprintf("%d\n", pid)), 0o644); err != nil {
		return "", err
	}

	return cgroupDir, nil
}

func normalizeCgroupPath(cgroupPath string) (string, error) {
	normalized := strings.TrimPrefix(filepath.Clean("/"+cgroupPath), "/")
	if normalized == "" || normalized == "." || strings.HasPrefix(normalized, "..") || strings.Contains(normalized, "/../") {
		return "", fmt.Errorf("--cgroup-path must be a relative cgroup v2 path")
	}
	return normalized, nil
}

func recordCreateFailure(collector *Collector, err error) error {
	event := Event{
		"event_type": "sandbox.firecracker.provider.create.failed",
		"source":     "firecracker_host_collector",
		"status":     "error",
	}
	for key, value := range ErrorFields(err) {
		event[key] = value
	}
	_ = collector.Record(event)
	return err
}

func bootArgs(config FirecrackerConfig) string {
	if config.BootArgs != "" {
		return config.BootArgs
	}
	return DefaultBootArgs
}

func guestMAC(config FirecrackerConfig) string {
	if config.GuestMAC != "" {
		return config.GuestMAC
	}
	return "AA:FC:00:00:00:01"
}

func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}
