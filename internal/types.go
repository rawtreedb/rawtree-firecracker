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

type VsockConfig struct {
	GuestCID uint32
	Port     uint32
}

type SandboxLaunchRequest struct {
	CgroupPath                 string
	Firecracker                FirecrackerConfig
	GuestAgentBinary           string
	HypervisorSampleIntervalMS int
	Metadata                   map[string]string
	MetricsFlushIntervalMS     int
	Provider                   string
	RawTree                    RawTreeConfig
	RootFS                     string
	RunID                      string
	RunTimeoutMS               int
	SandboxID                  string
	Vsock                      VsockConfig
}

type RuntimePaths struct {
	APISocketPath          string
	FirecrackerLogPath     string
	FirecrackerMetricsPath string
	RootFSCopyPath         string
	VsockPath              string
	WorkspaceDir           string
}

type Event map[string]any

type SandboxState struct {
	APISocketPath          string            `json:"api_socket_path"`
	BaseURL                string            `json:"base_url"`
	CgroupPath             string            `json:"cgroup_path,omitempty"`
	CreatedAt              string            `json:"created_at"`
	FirecrackerLogPath     string            `json:"firecracker_log_path"`
	FirecrackerMetricsPath string            `json:"firecracker_metrics_path"`
	FirecrackerPID         int               `json:"firecracker_pid"`
	Metadata               map[string]string `json:"metadata,omitempty"`
	Provider               string            `json:"provider"`
	RootFSPath             string            `json:"rootfs_path"`
	RunID                  string            `json:"run_id"`
	SandboxID              string            `json:"sandbox_id"`
	StateFilePath          string            `json:"state_file_path"`
	Status                 string            `json:"status"`
	StoppedAt              string            `json:"stopped_at,omitempty"`
	SupervisorPID          int               `json:"supervisor_pid"`
	Table                  string            `json:"table"`
	VsockPath              string            `json:"vsock_path"`
	VsockPort              uint32            `json:"vsock_port"`
	WorkspaceDir           string            `json:"workspace_dir"`
}

type ExecRequest struct {
	Argv        []string          `json:"argv"`
	Env         map[string]string `json:"env,omitempty"`
	Interactive bool              `json:"interactive,omitempty"`
	Sudo        bool              `json:"sudo,omitempty"`
	Workdir     string            `json:"workdir,omitempty"`
}

type ExecFrame struct {
	Data     string `json:"data,omitempty"`
	ExitCode int    `json:"exit_code,omitempty"`
	Message  string `json:"message,omitempty"`
	PID      int    `json:"pid,omitempty"`
	Type     string `json:"type"`
}
