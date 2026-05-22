package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/google/uuid"

	observability "github.com/rawtreedb/rawtree-firecracker/internal"
)

const (
	defaultBaseURL                    = "https://api.rawtree.com"
	defaultProvider                   = "firecracker-sandbox-provider"
	defaultTable                      = "sandbox_events"
	defaultRunTimeoutMS               = 30000
	defaultHypervisorSampleIntervalMS = 1000
	defaultMetricsFlushIntervalMS     = 0
	defaultSandboxTimeout             = time.Hour
	defaultVsockGuestCID              = 3
	defaultVsockPort                  = 1024
)

type metadataFlag map[string]string

func (m metadataFlag) String() string {
	encoded, _ := json.Marshal(map[string]string(m))
	return string(encoded)
}

func (m metadataFlag) Set(value string) error {
	for index, char := range value {
		if char == '=' {
			m[value[:index]] = value[index+1:]
			return nil
		}
	}
	return fmt.Errorf("--metadata must use key=value")
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "create":
			return runCreate(os.Args[2:])
		case "exec":
			return runExec(os.Args[2:])
		case "stop":
			return runStop(os.Args[2:])
		case "supervise":
			return runSupervisor(os.Args[2:])
		case "agent":
			return runAgent(os.Args[2:])
		case "help", "--help", "-h":
			printRootUsage(os.Stdout)
			return nil
		}
	}

	return runOneShot(os.Args[1:])
}

func runOneShot(argv []string) error {
	args := parseArgs(argv)
	request := sandboxLaunchRequestFromArgs(args)

	if args.dryRun {
		encoded, err := json.MarshalIndent(dryRunPlan(request), "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(encoded))
		return nil
	}

	if err := observability.AssertCanRunFirecracker(); err != nil {
		return err
	}

	return observability.LaunchObservedSandbox(context.Background(), request)
}

type createArgs struct {
	apiKey                     string
	baseURL                    string
	bootArgs                   string
	cgroupPath                 string
	connect                    bool
	firecracker                string
	guestMAC                   string
	guestCID                   int
	hypervisorSampleIntervalMS int
	kernel                     string
	memMiB                     int
	metadata                   map[string]string
	metricsFlushIntervalMS     int
	project                    string
	provider                   string
	rootfs                     string
	runtime                    string
	sandboxID                  string
	table                      string
	tap                        string
	timeout                    time.Duration
	vcpuCount                  int
	vsockPort                  int
}

type execArgs struct {
	apiKey      string
	baseURL     string
	env         map[string]string
	interactive bool
	sandboxID   string
	sudo        bool
	table       string
	workdir     string
	argv        []string
}

type stopArgs struct {
	apiKey     string
	baseURL    string
	project    string
	table      string
	wait       bool
	sandboxIDs []string
}

func runCreate(argv []string) error {
	args := parseCreateArgs(argv)
	if err := observability.AssertCanRunFirecracker(); err != nil {
		return err
	}

	statePath := observability.StatePath(args.sandboxID)
	if _, err := os.Stat(statePath); err == nil {
		return fmt.Errorf("sandbox already exists: %s", args.sandboxID)
	}

	agentPath, err := buildGuestAgentBinary()
	if err != nil {
		return err
	}

	request := sandboxLaunchRequestFromCreateArgs(args, agentPath)
	requestPath, err := writeSupervisorRequest(request)
	if err != nil {
		return err
	}

	logPath := statePath + ".supervisor.log"
	if err := startSupervisorProcess(requestPath, statePath, logPath); err != nil {
		return err
	}

	state, err := waitForSandboxRunning(context.Background(), args.sandboxID, 90*time.Second)
	if err != nil {
		return fmt.Errorf("wait for sandbox %s: %w; supervisor log: %s", args.sandboxID, err, logPath)
	}

	if err := waitForGuestAgent(context.Background(), state, args.apiKey); err != nil {
		return fmt.Errorf("wait for guest agent: %w; supervisor log: %s", err, logPath)
	}

	fmt.Printf("sandbox_id=%s\n", state.SandboxID)
	fmt.Printf("run_id=%s\n", state.RunID)
	fmt.Printf("state_file=%s\n", state.StateFilePath)

	if args.connect {
		_, err := observability.ExecInSandbox(context.Background(), state, observability.ExecOptions{
			RawTree: observability.RawTreeConfig{
				APIKey:  args.apiKey,
				BaseURL: args.baseURL,
				Table:   args.table,
			},
			Request: observability.ExecRequest{
				Argv:        []string{"sh"},
				Interactive: true,
			},
			Stdin:  os.Stdin,
			Stdout: os.Stdout,
			Stderr: os.Stderr,
		})
		return err
	}

	return nil
}

func runExec(argv []string) error {
	args := parseExecArgs(argv)
	state, err := observability.ReadState(args.sandboxID)
	if err != nil {
		return err
	}

	exitCode, err := observability.ExecInSandbox(context.Background(), state, observability.ExecOptions{
		RawTree: observability.RawTreeConfig{
			APIKey:  args.apiKey,
			BaseURL: args.baseURL,
			Table:   args.table,
		},
		Request: observability.ExecRequest{
			Argv:        args.argv,
			Env:         args.env,
			Interactive: args.interactive,
			Sudo:        args.sudo,
			Workdir:     args.workdir,
		},
		Stdin:  interactiveReader(args.interactive),
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	})
	if err != nil {
		return err
	}
	if exitCode != 0 {
		return exitError(exitCode)
	}
	return nil
}

func runStop(argv []string) error {
	args := parseStopArgs(argv)
	for _, sandboxID := range args.sandboxIDs {
		state, err := observability.ReadState(sandboxID)
		if err != nil {
			return err
		}
		if err := observability.StopSandbox(context.Background(), state, observability.RawTreeConfig{
			APIKey:  args.apiKey,
			BaseURL: args.baseURL,
			Table:   args.table,
		}); err != nil {
			return err
		}
		if args.wait {
			waitCtx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			stoppedState, err := observability.WaitForSandboxStopped(waitCtx, sandboxID, 500*time.Millisecond)
			cancel()
			if err != nil {
				return err
			}
			fmt.Printf("stopped sandbox_id=%s run_id=%s\n", stoppedState.SandboxID, stoppedState.RunID)
		} else {
			fmt.Printf("stop_sent sandbox_id=%s\n", sandboxID)
		}
	}
	return nil
}

func runSupervisor(argv []string) error {
	flags := flag.NewFlagSet("supervise", flag.ExitOnError)
	requestPath := flags.String("request-file", "", "Sandbox launch request JSON file")
	statePath := flags.String("state-file", "", "Sandbox state JSON file")
	_ = flags.Parse(argv)
	if *requestPath == "" || *statePath == "" {
		return fmt.Errorf("supervise requires --request-file and --state-file")
	}

	content, err := os.ReadFile(*requestPath)
	if err != nil {
		return err
	}
	request := observability.SandboxLaunchRequest{}
	if err := json.Unmarshal(content, &request); err != nil {
		return err
	}
	_ = os.Remove(*requestPath)

	if err := observability.AssertCanRunFirecracker(); err != nil {
		return err
	}
	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return observability.RunSandboxSupervisor(signalCtx, request, *statePath)
}

func runAgent(argv []string) error {
	flags := flag.NewFlagSet("agent", flag.ExitOnError)
	port := flags.Int("port", defaultVsockPort, "Guest vsock listen port")
	_ = flags.Parse(argv)
	if *port <= 0 {
		return fmt.Errorf("--port must be positive")
	}
	return runGuestAgent(uint32(*port))
}

type cliArgs struct {
	apiKey                     string
	baseURL                    string
	bootArgs                   string
	cgroupPath                 string
	dryRun                     bool
	firecracker                string
	guestMAC                   string
	hypervisorSampleIntervalMS int
	kernel                     string
	memMiB                     int
	metadata                   map[string]string
	metricsFlushIntervalMS     int
	provider                   string
	rootfs                     string
	runTimeoutMS               int
	sandboxID                  string
	table                      string
	tap                        string
	vcpuCount                  int
}

func parseArgs(argv []string) cliArgs {
	metadata := metadataFlag{}
	flags := flag.NewFlagSet("rawtree-firecracker-observability", flag.ExitOnError)
	flags.Usage = usage(flags)

	args := cliArgs{}
	flags.StringVar(&args.apiKey, "api-key", envFirst("RAWTREE_API_KEY", "RAWTREE_TOKEN"), "RawTree API key; host collector only")
	flags.StringVar(&args.baseURL, "base-url", envDefault("RAWTREE_BASE_URL", defaultBaseURL), "RawTree API base URL")
	flags.StringVar(&args.bootArgs, "boot-args", "", "Kernel boot args")
	flags.StringVar(&args.cgroupPath, "cgroup-path", "", "Optional cgroup v2 path for the Firecracker process")
	flags.BoolVar(&args.dryRun, "dry-run", false, "Print the provider integration plan without starting Firecracker")
	flags.StringVar(&args.firecracker, "firecracker", "firecracker", "Firecracker binary path")
	flags.StringVar(&args.guestMAC, "guest-mac", "", "Guest MAC for optional TAP device")
	flags.IntVar(&args.hypervisorSampleIntervalMS, "hypervisor-sample-interval-ms", defaultHypervisorSampleIntervalMS, "Host process/cgroup sample interval")
	flags.StringVar(&args.kernel, "kernel", "", "Kernel image path")
	flags.IntVar(&args.memMiB, "mem-mib", 512, "Memory in MiB")
	flags.Var(metadata, "metadata", "Provider metadata key=value; can be passed multiple times")
	flags.IntVar(&args.metricsFlushIntervalMS, "metrics-flush-interval-ms", defaultMetricsFlushIntervalMS, "Periodic Firecracker FlushMetrics interval; 0 disables it")
	flags.StringVar(&args.provider, "provider", defaultProvider, "Provider name")
	flags.StringVar(&args.rootfs, "rootfs", "", "Rootfs image path")
	flags.IntVar(&args.runTimeoutMS, "run-timeout-ms", defaultRunTimeoutMS, "Demo stop timeout before FlushMetrics and StopVMM")
	flags.StringVar(&args.sandboxID, "sandbox-id", "sbx_"+uuid.NewString(), "Existing internal sandbox id")
	flags.StringVar(&args.table, "table", envDefault("RAWTREE_SANDBOX_TABLE", defaultTable), "RawTree table")
	flags.StringVar(&args.tap, "tap", "", "Optional TAP device name")
	flags.IntVar(&args.vcpuCount, "vcpu-count", 1, "vCPU count")
	_ = flags.Parse(argv)

	args.metadata = map[string]string(metadata)
	if args.apiKey == "" && !args.dryRun {
		exitUsage(flags, "missing --api-key or RAWTREE_API_KEY")
	}
	if args.kernel == "" {
		exitUsage(flags, "missing --kernel")
	}
	if args.rootfs == "" {
		exitUsage(flags, "missing --rootfs")
	}
	if args.hypervisorSampleIntervalMS <= 0 {
		exitUsage(flags, "--hypervisor-sample-interval-ms must be a positive integer")
	}
	if args.metricsFlushIntervalMS < 0 {
		exitUsage(flags, "--metrics-flush-interval-ms must be a non-negative integer")
	}
	if args.memMiB <= 0 {
		exitUsage(flags, "--mem-mib must be a positive integer")
	}
	if args.runTimeoutMS <= 0 {
		exitUsage(flags, "--run-timeout-ms must be a positive integer")
	}
	if args.vcpuCount <= 0 {
		exitUsage(flags, "--vcpu-count must be a positive integer")
	}

	if args.apiKey == "" {
		args.apiKey = "dry-run-api-key-not-used"
	}

	return args
}

func sandboxLaunchRequestFromArgs(args cliArgs) observability.SandboxLaunchRequest {
	return observability.SandboxLaunchRequest{
		CgroupPath: args.cgroupPath,
		Firecracker: observability.FirecrackerConfig{
			Binary:    args.firecracker,
			BootArgs:  args.bootArgs,
			GuestMAC:  args.guestMAC,
			Kernel:    args.kernel,
			MemMiB:    int64(args.memMiB),
			Tap:       args.tap,
			VCPUCount: int64(args.vcpuCount),
		},
		HypervisorSampleIntervalMS: args.hypervisorSampleIntervalMS,
		Metadata:                   args.metadata,
		MetricsFlushIntervalMS:     args.metricsFlushIntervalMS,
		Provider:                   args.provider,
		RawTree: observability.RawTreeConfig{
			APIKey:  args.apiKey,
			BaseURL: args.baseURL,
			Table:   args.table,
		},
		RootFS:       args.rootfs,
		RunID:        "rt_firecracker_sandbox_run_" + uuid.NewString(),
		RunTimeoutMS: args.runTimeoutMS,
		SandboxID:    args.sandboxID,
	}
}

func dryRunPlan(request observability.SandboxLaunchRequest) map[string]any {
	firecrackerCalls := []string{
		"firecracker-go-sdk Config.LogPath -> PUT /logger",
		"firecracker-go-sdk Config.MetricsPath -> PUT /metrics",
		"firecracker-go-sdk Config.MachineCfg -> PUT /machine-config",
		"firecracker-go-sdk Config.KernelImagePath -> PUT /boot-source",
		"firecracker-go-sdk Config.Drives -> PUT /drives/rootfs",
	}
	if request.Firecracker.Tap != "" {
		firecrackerCalls = append(firecrackerCalls, "firecracker-go-sdk NetworkInterfaces -> PUT /network-interfaces")
	}
	firecrackerCalls = append(firecrackerCalls,
		"firecracker-go-sdk Machine.Start -> PUT /actions InstanceStart",
		"firecracker-go-sdk Client.CreateSyncAction -> PUT /actions FlushMetrics before provider stop",
	)

	return map[string]any{
		"architecture": "provider-side Firecracker observability via firecracker-go-sdk",
		"firecracker_native_outputs": map[string]string{
			"logger":  "Firecracker writes VMM logs to a host file configured through the Go SDK.",
			"metrics": "Firecracker writes VMM/device metrics JSON to a host file configured through the Go SDK.",
		},
		"firecracker_calls": firecrackerCalls,
		"hypervisor_samples": map[string]any{
			"interval_ms": request.HypervisorSampleIntervalMS,
			"source":      "/proc/<firecracker-pid> and cgroup v2 files on the host",
		},
		"metadata":                  request.Metadata,
		"metrics_flush_interval_ms": request.MetricsFlushIntervalMS,
		"provider":                  request.Provider,
		"rawtree_api_key_location":  "host collector only",
		"rootfs_source":             request.RootFS,
		"run_id":                    request.RunID,
		"run_timeout_ms":            request.RunTimeoutMS,
		"sandbox_cgroup_path":       nullIfEmpty(request.CgroupPath),
		"sandbox_id":                request.SandboxID,
	}
}

func usage(flags *flag.FlagSet) func() {
	return func() {
		fmt.Fprintf(flags.Output(), "Usage:\n")
		fmt.Fprintf(flags.Output(), "  sudo -E go run . \\\n")
		fmt.Fprintf(flags.Output(), "    --firecracker /usr/local/bin/firecracker \\\n")
		fmt.Fprintf(flags.Output(), "    --kernel /var/lib/firecracker/vmlinux \\\n")
		fmt.Fprintf(flags.Output(), "    --rootfs /var/lib/firecracker/rootfs.ext4\n\n")
		fmt.Fprintf(flags.Output(), "Dry run:\n")
		fmt.Fprintf(flags.Output(), "  go run . --dry-run --kernel /var/lib/firecracker/vmlinux --rootfs /var/lib/firecracker/rootfs.ext4\n\n")
		fmt.Fprintf(flags.Output(), "Options:\n")
		flags.PrintDefaults()
	}
}

func exitUsage(flags *flag.FlagSet, message string) {
	fmt.Fprintln(os.Stderr, message)
	flags.Usage()
	os.Exit(2)
}

func envDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envFirst(names ...string) string {
	for _, name := range names {
		if value := os.Getenv(name); value != "" {
			return value
		}
	}
	return ""
}

func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func parseCreateArgs(argv []string) createArgs {
	metadata := metadataFlag{}
	flags := flag.NewFlagSet("create", flag.ExitOnError)
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Usage:\n")
		fmt.Fprintf(flags.Output(), "  go run . create --runtime node --timeout 1h\n\n")
		flags.PrintDefaults()
	}

	args := createArgs{}
	timeout := ""
	flags.StringVar(&args.apiKey, "api-key", envFirst("RAWTREE_API_KEY", "RAWTREE_TOKEN"), "RawTree API key; host collector only")
	flags.StringVar(&args.baseURL, "base-url", envDefault("RAWTREE_BASE_URL", defaultBaseURL), "RawTree API base URL")
	flags.StringVar(&args.bootArgs, "boot-args", "", "Kernel boot args")
	flags.StringVar(&args.cgroupPath, "cgroup-path", "", "Optional cgroup v2 path for the Firecracker process")
	flags.BoolVar(&args.connect, "connect", false, "Open an interactive shell after the sandbox is ready")
	flags.StringVar(&args.firecracker, "firecracker", "/usr/local/bin/firecracker", "Firecracker binary path")
	flags.StringVar(&args.guestMAC, "guest-mac", "", "Guest MAC for optional TAP device")
	flags.IntVar(&args.guestCID, "guest-cid", defaultVsockGuestCID, "Guest vsock CID")
	flags.IntVar(&args.hypervisorSampleIntervalMS, "hypervisor-sample-interval-ms", defaultHypervisorSampleIntervalMS, "Host process/cgroup sample interval")
	flags.StringVar(&args.kernel, "kernel", envDefault("KERNEL", "/var/lib/firecracker/vmlinux"), "Kernel image path")
	flags.IntVar(&args.memMiB, "mem-mib", 512, "Memory in MiB")
	flags.Var(metadata, "metadata", "Provider metadata key=value; can be passed multiple times")
	flags.IntVar(&args.metricsFlushIntervalMS, "metrics-flush-interval-ms", 2000, "Periodic Firecracker FlushMetrics interval")
	flags.StringVar(&args.project, "project", "", "Project metadata")
	flags.StringVar(&args.provider, "provider", defaultProvider, "Provider name")
	flags.StringVar(&args.rootfs, "rootfs", envDefault("BASE_ROOTFS", "/var/lib/firecracker/rootfs.ext4"), "Base rootfs image path")
	flags.StringVar(&args.runtime, "runtime", "node", "Runtime metadata, for example node or python3.13")
	flags.StringVar(&args.sandboxID, "sandbox-id", "sb_"+uuid.NewString(), "Sandbox id")
	flags.StringVar(&args.table, "table", envDefault("RAWTREE_SANDBOX_TABLE", defaultTable), "RawTree table")
	flags.StringVar(&args.tap, "tap", "", "Optional TAP device name")
	flags.StringVar(&timeout, "timeout", defaultSandboxTimeout.String(), "Sandbox lifetime, for example 30m or 1h")
	flags.IntVar(&args.vcpuCount, "vcpus", 1, "vCPU count")
	flags.IntVar(&args.vsockPort, "vsock-port", defaultVsockPort, "Guest agent vsock port")
	_ = flags.Parse(argv)

	if args.apiKey == "" {
		exitUsage(flags, "missing --api-key or RAWTREE_API_KEY")
	}
	if args.kernel == "" {
		exitUsage(flags, "missing --kernel")
	}
	if args.rootfs == "" {
		exitUsage(flags, "missing --rootfs")
	}
	if args.vcpuCount <= 0 {
		exitUsage(flags, "--vcpus must be a positive integer")
	}
	if args.memMiB <= 0 {
		exitUsage(flags, "--mem-mib must be a positive integer")
	}
	if args.guestCID < 3 {
		exitUsage(flags, "--guest-cid must be >= 3")
	}
	if args.vsockPort <= 0 {
		exitUsage(flags, "--vsock-port must be a positive integer")
	}
	parsedTimeout, err := time.ParseDuration(timeout)
	if err != nil {
		exitUsage(flags, "--timeout must be a Go duration such as 30m or 1h")
	}
	args.timeout = parsedTimeout

	args.metadata = map[string]string(metadata)
	args.metadata["runtime"] = args.runtime
	if args.project != "" {
		args.metadata["project"] = args.project
	}
	return args
}

func parseExecArgs(argv []string) execArgs {
	env := metadataFlag{}
	flags := flag.NewFlagSet("exec", flag.ExitOnError)
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Usage:\n")
		fmt.Fprintf(flags.Output(), "  go run . exec sb_123 ls -la\n")
		fmt.Fprintf(flags.Output(), "  go run . exec --env DEBUG=true --workdir /app sb_123 npm test\n\n")
		flags.PrintDefaults()
	}

	args := execArgs{}
	flags.StringVar(&args.apiKey, "api-key", envFirst("RAWTREE_API_KEY", "RAWTREE_TOKEN"), "RawTree API key")
	flags.StringVar(&args.baseURL, "base-url", envDefault("RAWTREE_BASE_URL", defaultBaseURL), "RawTree API base URL")
	flags.Var(env, "env", "Environment key=value; can be passed multiple times")
	flags.BoolVar(&args.interactive, "interactive", false, "Attach stdin to the command")
	flags.BoolVar(&args.sudo, "sudo", false, "Run through sudo when the guest agent is not already root")
	flags.StringVar(&args.table, "table", envDefault("RAWTREE_SANDBOX_TABLE", defaultTable), "RawTree table")
	flags.StringVar(&args.workdir, "workdir", "", "Working directory inside the sandbox")
	_ = flags.Parse(argv)

	remaining := flags.Args()
	if args.apiKey == "" {
		exitUsage(flags, "missing --api-key or RAWTREE_API_KEY")
	}
	if len(remaining) < 2 {
		exitUsage(flags, "exec requires a sandbox id and a command")
	}
	args.env = map[string]string(env)
	args.sandboxID = remaining[0]
	args.argv = remaining[1:]
	return args
}

func parseStopArgs(argv []string) stopArgs {
	flags := flag.NewFlagSet("stop", flag.ExitOnError)
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Usage:\n")
		fmt.Fprintf(flags.Output(), "  go run . stop sb_123 sb_456\n\n")
		flags.PrintDefaults()
	}

	args := stopArgs{}
	flags.StringVar(&args.apiKey, "api-key", envFirst("RAWTREE_API_KEY", "RAWTREE_TOKEN"), "RawTree API key")
	flags.StringVar(&args.baseURL, "base-url", envDefault("RAWTREE_BASE_URL", defaultBaseURL), "RawTree API base URL")
	flags.StringVar(&args.project, "project", "", "Project metadata filter; stored for API parity")
	flags.StringVar(&args.table, "table", envDefault("RAWTREE_SANDBOX_TABLE", defaultTable), "RawTree table")
	flags.BoolVar(&args.wait, "wait", true, "Wait for the supervisor to drain events and mark the sandbox stopped")
	_ = flags.Parse(argv)

	if args.apiKey == "" {
		exitUsage(flags, "missing --api-key or RAWTREE_API_KEY")
	}
	args.sandboxIDs = flags.Args()
	if len(args.sandboxIDs) == 0 {
		exitUsage(flags, "stop requires at least one sandbox id")
	}
	return args
}

func sandboxLaunchRequestFromCreateArgs(args createArgs, guestAgentBinary string) observability.SandboxLaunchRequest {
	return observability.SandboxLaunchRequest{
		CgroupPath: args.cgroupPath,
		Firecracker: observability.FirecrackerConfig{
			Binary:    args.firecracker,
			BootArgs:  args.bootArgs,
			GuestMAC:  args.guestMAC,
			Kernel:    args.kernel,
			MemMiB:    int64(args.memMiB),
			Tap:       args.tap,
			VCPUCount: int64(args.vcpuCount),
		},
		GuestAgentBinary:           guestAgentBinary,
		HypervisorSampleIntervalMS: args.hypervisorSampleIntervalMS,
		Metadata:                   args.metadata,
		MetricsFlushIntervalMS:     args.metricsFlushIntervalMS,
		Provider:                   args.provider,
		RawTree: observability.RawTreeConfig{
			APIKey:  args.apiKey,
			BaseURL: args.baseURL,
			Table:   args.table,
		},
		RootFS:       args.rootfs,
		RunID:        "rt_firecracker_sandbox_run_" + uuid.NewString(),
		RunTimeoutMS: int(args.timeout / time.Millisecond),
		SandboxID:    args.sandboxID,
		Vsock: observability.VsockConfig{
			GuestCID: uint32(args.guestCID),
			Port:     uint32(args.vsockPort),
		},
	}
}

func buildGuestAgentBinary() (string, error) {
	outputDir := filepath.Join(os.TempDir(), "rawtree-firecracker-agents")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return "", err
	}
	outputPath := filepath.Join(outputDir, "rawtree-sandbox-"+strconv.Itoa(os.Getpid()))

	cmd := exec.Command(envDefault("GO_BIN", "go"), "build", "-o", outputPath, ".")
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("build guest agent: %w", err)
	}
	return outputPath, nil
}

func writeSupervisorRequest(request observability.SandboxLaunchRequest) (string, error) {
	if err := os.MkdirAll(observability.StateDir(), 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(observability.StateDir(), request.SandboxID+".request.json")
	encoded, err := json.MarshalIndent(request, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, append(encoded, '\n'), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func startSupervisorProcess(requestPath, statePath, logPath string) error {
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer logFile.Close()

	cmd := exec.Command(os.Args[0], "supervise", "--request-file", requestPath, "--state-file", statePath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func waitForSandboxRunning(ctx context.Context, sandboxID string, timeout time.Duration) (observability.SandboxState, error) {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		state, err := observability.ReadState(sandboxID)
		if err == nil && state.Status == "running" {
			return state, nil
		}

		select {
		case <-waitCtx.Done():
			if err != nil {
				return observability.SandboxState{}, err
			}
			return state, waitCtx.Err()
		case <-ticker.C:
		}
	}
}

func waitForGuestAgent(ctx context.Context, state observability.SandboxState, apiKey string) error {
	waitCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	var lastErr error
	for {
		exitCode, err := observability.ExecInSandbox(waitCtx, state, observability.ExecOptions{
			RawTree: observability.RawTreeConfig{
				APIKey:  apiKey,
				BaseURL: state.BaseURL,
				Table:   state.Table,
			},
			Request: observability.ExecRequest{
				Argv: []string{"sh", "-lc", "true"},
			},
			Stdout:            io.Discard,
			Stderr:            io.Discard,
			SuppressTelemetry: true,
		})
		if err == nil && exitCode == 0 {
			return nil
		}
		if err != nil {
			lastErr = err
		}

		select {
		case <-waitCtx.Done():
			if lastErr != nil {
				return lastErr
			}
			return waitCtx.Err()
		case <-ticker.C:
		}
	}
}

func interactiveReader(enabled bool) io.Reader {
	if enabled {
		return os.Stdin
	}
	return nil
}

type exitError int

func (e exitError) Error() string {
	return fmt.Sprintf("command exited with status %d", int(e))
}

func printRootUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  go run . create [options]")
	fmt.Fprintln(w, "  go run . exec [options] <sandbox-id> <command> [args...]")
	fmt.Fprintln(w, "  go run . stop [options] <sandbox-id> [sandbox-id...]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Legacy one-shot mode is still available with the root flags.")
}
