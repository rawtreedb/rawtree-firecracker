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
  firecracker: FirecrackerConfig;
  metadata: Record<string, string>;
  provider: string;
  rawtree: RawTreeConfig;
  rootfs: string;
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

export type RawTreeEvent = {
  event_id?: string;
  event_time?: string;
  event_type: string;
  provider?: string;
  run_id?: string;
  sandbox_id?: string;
  source?: string;
  status?: string;
  [key: string]: string | number | boolean | null | undefined;
};
