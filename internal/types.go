package observability

type RawTreeConfig struct {
	APIKey  string
	BaseURL string
	Table   string
}

type FirecrackerConfig struct {
	Binary    string
	BootArgs  string
	GuestMAC  string
	Kernel    string
	MemMiB    int64
	Tap       string
	VCPUCount int64
}

type SandboxLaunchRequest struct {
	CgroupPath                 string
	Firecracker                FirecrackerConfig
	HypervisorSampleIntervalMS int
	Metadata                   map[string]string
	MetricsFlushIntervalMS     int
	Provider                   string
	RawTree                    RawTreeConfig
	RootFS                     string
	RunID                      string
	RunTimeoutMS               int
	SandboxID                  string
}

type RuntimePaths struct {
	APISocketPath          string
	FirecrackerLogPath     string
	FirecrackerMetricsPath string
	RootFSCopyPath         string
	WorkspaceDir           string
}

type Event map[string]any
