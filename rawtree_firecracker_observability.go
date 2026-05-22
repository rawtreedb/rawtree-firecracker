package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/google/uuid"

	observability "github.com/rawtreedb/firecracker-observability/internal"
)

const (
	defaultBaseURL                    = "https://api.rawtree.com"
	defaultProvider                   = "firecracker-sandbox-provider"
	defaultTable                      = "sandbox_events"
	defaultRunTimeoutMS               = 30000
	defaultHypervisorSampleIntervalMS = 1000
	defaultMetricsFlushIntervalMS     = 0
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
	args := parseArgs(os.Args[1:])
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
