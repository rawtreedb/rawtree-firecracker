//go:build linux

package observability

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
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
		flushCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = flushMetrics(flushCtx, client)
		cancel()
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

func RunSandboxSupervisor(ctx context.Context, request SandboxLaunchRequest, statePath string) error {
	paths, err := runtimePaths(request.SandboxID)
	if err != nil {
		return err
	}

	var (
		cgroupDir      string
		machine        *firecracker.Machine
		metricsStopper func()
		sampler        *HypervisorSampler
		state          SandboxState
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

	if err := prepareSandboxRootFS(request.RootFS, paths.RootFSCopyPath, request.GuestAgentBinary, request.Vsock.Port); err != nil {
		return err
	}

	if err := collector.Record(Event{
		"event_type": "sandbox.firecracker.provider.create.started",
		"source":     "firecracker_host_collector",
		"status":     "started",
		"vsock": map[string]any{
			"guest_cid": request.Vsock.GuestCID,
			"port":      request.Vsock.Port,
		},
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

	state = SandboxState{
		APISocketPath:          paths.APISocketPath,
		BaseURL:                request.RawTree.BaseURL,
		CgroupPath:             request.CgroupPath,
		CreatedAt:              time.Now().UTC().Format(time.RFC3339Nano),
		FirecrackerLogPath:     paths.FirecrackerLogPath,
		FirecrackerMetricsPath: paths.FirecrackerMetricsPath,
		FirecrackerPID:         pid,
		Metadata:               request.Metadata,
		Provider:               request.Provider,
		RootFSPath:             paths.RootFSCopyPath,
		RunID:                  request.RunID,
		SandboxID:              request.SandboxID,
		StateFilePath:          statePath,
		Status:                 "running",
		SupervisorPID:          os.Getpid(),
		Table:                  request.RawTree.Table,
		VsockPath:              paths.VsockPath,
		VsockPort:              request.Vsock.Port,
		WorkspaceDir:           paths.WorkspaceDir,
	}
	if err := WriteState(statePath, state); err != nil {
		return err
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
		"vsock": map[string]any{
			"guest_cid": request.Vsock.GuestCID,
			"host_path": paths.VsockPath,
			"path":      paths.VsockPath,
			"port":      request.Vsock.Port,
		},
	}); err != nil {
		return err
	}

	fmt.Println("Firecracker sandbox started")
	fmt.Printf("Sandbox id: %s\n", request.SandboxID)
	fmt.Printf("Run id: %s\n", request.RunID)
	fmt.Printf("Vsock path: %s\n", paths.VsockPath)

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- machine.Wait(ctx)
	}()

	stopReason := "firecracker_exit"
	exitCode := 0

	stopAndWait := func(reason string) {
		stopReason = reason
		if metricsStopper != nil {
			metricsStopper()
			metricsStopper = nil
		}
		if sampler != nil {
			sampler.Sample()
		}
		flushCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = flushMetrics(flushCtx, client)
		cancel()
		if err := machine.StopVMM(); err != nil {
			exitCode = 1
		}
		select {
		case err := <-waitCh:
			if err != nil && reason != "provider_stop_requested" {
				exitCode = 1
			}
		case <-time.After(5 * time.Second):
			exitCode = 1
		}
	}

	if request.RunTimeoutMS > 0 {
		select {
		case err := <-waitCh:
			if err != nil {
				exitCode = 1
			}
		case <-time.After(time.Duration(request.RunTimeoutMS) * time.Millisecond):
			stopAndWait("run_timeout_reached")
		case <-ctx.Done():
			stopAndWait("provider_stop_requested")
		}
	} else {
		select {
		case err := <-waitCh:
			if err != nil {
				exitCode = 1
			}
		case <-ctx.Done():
			stopAndWait("provider_stop_requested")
		}
	}

	if _, err := os.Stat(providerStopMarkerPath(paths)); err == nil {
		stopReason = "provider_stop_requested"
		exitCode = 0
	}

	if metricsStopper != nil {
		metricsStopper()
		metricsStopper = nil
	}
	if sampler != nil {
		sampler.Stop()
		sampler = nil
	}

	state.Status = "stopped"
	state.StoppedAt = time.Now().UTC().Format(time.RFC3339Nano)
	_ = WriteState(statePath, state)

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

	_ = WriteState(statePath, state)

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

	if request.Vsock.GuestCID > 0 && request.Vsock.Port > 0 {
		cfg.VsockDevices = []firecracker.VsockDevice{
			{
				ID:   "control",
				Path: paths.VsockPath,
				CID:  request.Vsock.GuestCID,
			},
		}
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
		VsockPath:              filepath.Join(workspaceDir, "control.vsock"),
		WorkspaceDir:           workspaceDir,
	}, nil
}

func prepareSandboxRootFS(source, destination, guestAgentBinary string, vsockPort uint32) error {
	if guestAgentBinary == "" {
		return copyFile(source, destination)
	}
	if vsockPort == 0 {
		vsockPort = 1024
	}

	if err := copyFile(source, destination); err != nil {
		return err
	}

	mountDir, err := os.MkdirTemp("", "rawtree-sandbox-rootfs-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(mountDir)

	if output, err := exec.Command("mount", "-o", "loop", destination, mountDir).CombinedOutput(); err != nil {
		return fmt.Errorf("mount rootfs: %w: %s", err, truncate(string(output), 500))
	}
	mounted := true
	defer func() {
		if mounted {
			_ = exec.Command("umount", mountDir).Run()
		}
	}()

	agentPath := filepath.Join(mountDir, "usr/local/bin/rawtree-sandbox")
	if err := os.MkdirAll(filepath.Dir(agentPath), 0o755); err != nil {
		return err
	}
	if err := copyFile(guestAgentBinary, agentPath); err != nil {
		return err
	}
	if err := os.Chmod(agentPath, 0o755); err != nil {
		return err
	}

	servicePath := filepath.Join(mountDir, "etc/systemd/system/rawtree-sandbox-agent.service")
	if err := os.MkdirAll(filepath.Dir(servicePath), 0o755); err != nil {
		return err
	}
	service := fmt.Sprintf(`[Unit]
Description=RawTree sandbox provider control agent
After=multi-user.target

[Service]
Type=simple
ExecStart=/usr/local/bin/rawtree-sandbox agent --port %d
Restart=always
RestartSec=1

[Install]
WantedBy=multi-user.target
`, vsockPort)
	if err := os.WriteFile(servicePath, []byte(service), 0o644); err != nil {
		return err
	}

	wantsDir := filepath.Join(mountDir, "etc/systemd/system/multi-user.target.wants")
	if err := os.MkdirAll(wantsDir, 0o755); err != nil {
		return err
	}
	linkPath := filepath.Join(wantsDir, "rawtree-sandbox-agent.service")
	_ = os.Remove(linkPath)
	if err := os.Symlink("../rawtree-sandbox-agent.service", linkPath); err != nil {
		return err
	}

	_ = exec.Command("sync").Run()
	if output, err := exec.Command("umount", mountDir).CombinedOutput(); err != nil {
		return fmt.Errorf("umount rootfs: %w: %s", err, truncate(string(output), 500))
	}
	mounted = false

	return nil
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

func providerStopMarkerPath(paths RuntimePaths) string {
	return filepath.Join(paths.WorkspaceDir, "provider-stop.requested")
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
