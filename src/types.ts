export type RawTreeConfig = {
  apiKey: string;
  baseUrl: string;
  table: string;
};

export type FirecrackerConfig = {
  binary: string;
  bootArgs?: string;
  guestMac?: string;
  kernel: string;
  memMiB: number;
  tap?: string;
  vcpuCount: number;
};

export type SandboxLaunchRequest = {
  cgroupPath?: string;
  firecracker: FirecrackerConfig;
  metadata: Record<string, string>;
  metricsFlushIntervalMs: number;
  provider: string;
  rawtree: RawTreeConfig;
  rootfs: string;
  hypervisorSampleIntervalMs: number;
  runId: string;
  runTimeoutMs: number;
  sandboxId: string;
};

export type RuntimePaths = {
  apiSocketPath: string;
  firecrackerLogPath: string;
  firecrackerMetricsPath: string;
  rootfsCopyPath: string;
  workspaceDir: string;
};

export type JsonValue =
  | string
  | number
  | boolean
  | null
  | JsonValue[]
  | JsonObject;

export type JsonObject = { [key: string]: JsonValue };

export type RawTreeEvent = {
  event_id?: string;
  event_time?: string;
  event_type: string;
  metadata?: Record<string, string>;
  provider?: string;
  run_id?: string;
  sampled_at?: string;
  sandbox_id?: string;
  source?: string;
  status?: string;
  [key: string]: JsonValue | undefined;
};
