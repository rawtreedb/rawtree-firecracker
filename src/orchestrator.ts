import { randomUUID } from "node:crypto";
import { spawn, type ChildProcess } from "node:child_process";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";

import { configureFirecrackerMicroVM, DEFAULT_BOOT_ARGS, firecrackerClient } from "./firecracker-api.js";
import {
  emitFirecrackerNativeEvents,
  prepareFirecrackerOutputFiles,
} from "./firecracker-native-collector.js";
import { startHostCollector, type HostCollector } from "./host-collector.js";
import { startHypervisorSampler, type HypervisorSampler } from "./hypervisor-sampler.js";
import type { FirecrackerConfig, RuntimePaths, SandboxLaunchRequest } from "./types.js";

type CliOptions = {
  apiKey: string;
  baseUrl: string;
  bootArgs?: string;
  cgroupPath?: string;
  dryRun: boolean;
  firecracker: string;
  guestMac?: string;
  hypervisorSampleIntervalMs: number;
  kernel: string;
  memMiB: number;
  metricsFlushIntervalMs: number;
  metadata: Record<string, string>;
  provider: string;
  rootfs: string;
  runTimeoutMs: number;
  sandboxId: string;
  table: string;
  tap?: string;
  vcpuCount: number;
};

type ParsedArgs =
  | { kind: "help" }
  | { kind: "run"; options: CliOptions };

const DEFAULT_BASE_URL = "https://api.rawtree.com";
const DEFAULT_PROVIDER = "firecracker-sandbox-provider";
const DEFAULT_TABLE = "sandbox_events";
const DEFAULT_RUN_TIMEOUT_MS = 30_000;
const DEFAULT_HYPERVISOR_SAMPLE_INTERVAL_MS = 1_000;
const DEFAULT_METRICS_FLUSH_INTERVAL_MS = 0;
const FIRECRACKER_EXIT_TIMEOUT_MS = 5_000;

async function main(): Promise<void> {
  const parsed = parseArgs(process.argv.slice(2));

  if (parsed.kind === "help") {
    console.log(usage());
    return;
  }

  const request = sandboxLaunchRequestFromOptions(parsed.options);

  if (parsed.options.dryRun) {
    console.log(JSON.stringify(dryRunPlan(request), null, 2));
    return;
  }

  assertCanRunFirecracker();
  await launchObservedSandbox(request);
}

async function launchObservedSandbox(request: SandboxLaunchRequest): Promise<void> {
  const paths = await runtimePaths(request.sandboxId);
  let firecracker: ChildProcess | undefined;
  let collector: HostCollector | undefined;
  let cgroupDir: string | undefined;
  let hypervisorSampler: HypervisorSampler | undefined;
  let stopMetricsFlusher: (() => Promise<void>) | undefined;

  try {
    await fs.copyFile(request.rootfs, paths.rootfsCopyPath);
    await prepareFirecrackerOutputFiles(paths);

    collector = await startHostCollector({ request });

    await collector.record({
      event_type: "sandbox.firecracker.provider.create.started",
      source: "firecracker_host_collector",
      status: "started",
    });

    firecracker = spawn(request.firecracker.binary, ["--api-sock", paths.apiSocketPath], {
      stdio: ["ignore", "ignore", "inherit"],
    });
    if (!firecracker.pid) {
      throw new Error("Firecracker process started without a host PID.");
    }
    const firecrackerExit = onceExit(firecracker);
    if (request.cgroupPath) {
      cgroupDir = await moveProcessToCgroup(firecracker.pid, request.cgroupPath);
    }
    hypervisorSampler = startHypervisorSampler({
      collector,
      intervalMs: request.hypervisorSampleIntervalMs,
      pid: firecracker.pid,
    });

    await waitForSocket(paths.apiSocketPath);
    const fc = firecrackerClient(paths.apiSocketPath);

    await fc.put("/logger", {
      level: "Info",
      log_path: paths.firecrackerLogPath,
      show_level: true,
      show_log_origin: true,
    });
    await fc.put("/metrics", {
      metrics_path: paths.firecrackerMetricsPath,
    });
    await configureFirecrackerMicroVM(fc, request.firecracker, paths.rootfsCopyPath);
    await fc.put("/actions", { action_type: "InstanceStart" });
    stopMetricsFlusher = startMetricsFlusher(fc, request.metricsFlushIntervalMs);

    await collector.record({
      event_type: "sandbox.firecracker.provider.vm.started",
      source: "firecracker_host_collector",
      status: "success",
      api_socket_path: paths.apiSocketPath,
      boot_args: request.firecracker.bootArgs ?? DEFAULT_BOOT_ARGS,
      firecracker_log_path: paths.firecrackerLogPath,
      firecracker_metrics_path: paths.firecrackerMetricsPath,
      firecracker_pid: firecracker.pid,
      sandbox_cgroup_path: request.cgroupPath ?? null,
      workspace_dir: paths.workspaceDir,
    });

    console.log("Firecracker sandbox started");
    console.log(`Sandbox id: ${request.sandboxId}`);
    console.log(`Run id: ${request.runId}`);
    console.log(`Firecracker log path: ${paths.firecrackerLogPath}`);
    console.log(`Firecracker metrics path: ${paths.firecrackerMetricsPath}`);
    console.log(`Workspace: ${paths.workspaceDir}`);

    const stopResult = await Promise.race([
      firecrackerExit.then((exitCode) => ({ exitCode, reason: "firecracker_exit" as const })),
      sleep(request.runTimeoutMs).then(() => ({ exitCode: 0, reason: "run_timeout_reached" as const })),
    ]);

    await stopMetricsFlusher?.();
    stopMetricsFlusher = undefined;

    if (stopResult.reason !== "firecracker_exit") {
      await hypervisorSampler.sample();
      await fc.put("/actions", { action_type: "FlushMetrics" }).catch(() => undefined);
      stopResult.exitCode = await terminateFirecracker(firecracker, firecrackerExit);
    }

    await hypervisorSampler.stop();
    await emitFirecrackerNativeEvents(paths, collector);

    const status = stopResult.reason === "firecracker_exit" && stopResult.exitCode !== 0 ? "error" : "success";
    await collector.record({
      event_type: "sandbox.firecracker.provider.vm.stopped",
      source: "firecracker_host_collector",
      status,
      exit_code: stopResult.exitCode,
      stop_reason: stopResult.reason,
    });
    await collector.flush();
    process.exitCode = status === "success" ? 0 : 1;
  } catch (error) {
    if (collector) {
      await collector.record({
        event_type: "sandbox.firecracker.provider.create.failed",
        source: "firecracker_host_collector",
        status: "error",
        ...errorFields(error),
      }).catch(() => undefined);
      await collector.flush().catch(() => undefined);
    }

    throw error;
  } finally {
    await stopMetricsFlusher?.();
    await hypervisorSampler?.stop();

    if (firecracker) {
      firecracker.kill("SIGTERM");
    }

    await collector?.close();
    if (cgroupDir) {
      await fs.rmdir(cgroupDir).catch(() => undefined);
    }
    await fs.rm(paths.workspaceDir, { force: true, recursive: true }).catch(() => undefined);
  }
}

function sandboxLaunchRequestFromOptions(options: CliOptions): SandboxLaunchRequest {
  const firecracker: FirecrackerConfig = {
    binary: options.firecracker,
    kernel: options.kernel,
    memMiB: options.memMiB,
    vcpuCount: options.vcpuCount,
  };

  if (options.bootArgs) {
    firecracker.bootArgs = options.bootArgs;
  }

  if (options.guestMac) {
    firecracker.guestMac = options.guestMac;
  }

  if (options.tap) {
    firecracker.tap = options.tap;
  }

  const request: SandboxLaunchRequest = {
    firecracker,
    metadata: options.metadata,
    metricsFlushIntervalMs: options.metricsFlushIntervalMs,
    provider: options.provider,
    rawtree: {
      apiKey: options.apiKey,
      baseUrl: options.baseUrl,
      table: options.table,
    },
    rootfs: options.rootfs,
    hypervisorSampleIntervalMs: options.hypervisorSampleIntervalMs,
    runId: `rt_firecracker_sandbox_run_${randomUUID()}`,
    runTimeoutMs: options.runTimeoutMs,
    sandboxId: options.sandboxId,
  };

  if (options.cgroupPath) {
    request.cgroupPath = options.cgroupPath;
  }

  return request;
}

async function runtimePaths(sandboxId: string): Promise<RuntimePaths> {
  const workspaceDir = await fs.mkdtemp(path.join(os.tmpdir(), `rawtree-${sandboxId}-`));
  return {
    apiSocketPath: path.join(workspaceDir, "firecracker.socket"),
    firecrackerLogPath: path.join(workspaceDir, "firecracker.log"),
    firecrackerMetricsPath: path.join(workspaceDir, "firecracker.metrics.jsonl"),
    rootfsCopyPath: path.join(workspaceDir, "rootfs.ext4"),
    workspaceDir,
  };
}

function dryRunPlan(request: SandboxLaunchRequest): Record<string, unknown> {
  return {
    architecture: "provider-side Firecracker API observability",
    firecracker_calls: [
      "PUT /logger",
      "PUT /metrics",
      "PUT /machine-config",
      "PUT /boot-source",
      "PUT /drives/rootfs",
      request.firecracker.tap ? "PUT /network-interfaces/eth0" : undefined,
      "PUT /actions InstanceStart",
      "PUT /actions FlushMetrics before provider stop",
    ].filter(Boolean),
    firecracker_native_outputs: {
      logger: "Firecracker writes VMM logs to a host file configured through /logger.",
      metrics: "Firecracker writes VMM/device metrics JSON to a host file configured through /metrics.",
    },
    hypervisor_samples: {
      interval_ms: request.hypervisorSampleIntervalMs,
      source: "/proc/<firecracker-pid> and cgroup v2 files on the host",
    },
    metrics_flush_interval_ms: request.metricsFlushIntervalMs,
    metadata: request.metadata,
    provider: request.provider,
    rawtree_api_key_location: "host collector only",
    rootfs_source: request.rootfs,
    sandbox_cgroup_path: request.cgroupPath,
    run_id: request.runId,
    run_timeout_ms: request.runTimeoutMs,
    sandbox_id: request.sandboxId,
  };
}

function assertCanRunFirecracker(): void {
  if (process.platform !== "linux") {
    throw new Error("This reference starts real Firecracker, so it must run on a Linux host with KVM.");
  }
}

async function waitForSocket(socketPath: string): Promise<void> {
  const startedAt = Date.now();

  while (Date.now() - startedAt < 5000) {
    try {
      await fs.stat(socketPath);
      return;
    } catch {
      await sleep(50);
    }
  }

  throw new Error(`Timed out waiting for Firecracker API socket: ${socketPath}`);
}

function onceExit(child: ChildProcess): Promise<number> {
  return new Promise((resolve) => {
    child.once("error", () => resolve(1));
    child.once("exit", (code) => resolve(code ?? 1));
  });
}

async function terminateFirecracker(child: ChildProcess, firecrackerExit: Promise<number>): Promise<number> {
  child.kill("SIGTERM");
  const sigtermExit = await Promise.race([
    firecrackerExit,
    sleep(FIRECRACKER_EXIT_TIMEOUT_MS).then(() => undefined),
  ]);

  if (typeof sigtermExit === "number") {
    return sigtermExit;
  }

  child.kill("SIGKILL");
  return await Promise.race([
    firecrackerExit,
    sleep(2000).then(() => 1),
  ]);
}

function parseArgs(args: string[]): ParsedArgs {
  const values = new Map<string, string>();
  const metadata: Record<string, string> = {};
  let dryRun = false;

  for (let index = 0; index < args.length; index += 1) {
    const arg = args[index];

    if (arg === "--help" || arg === "-h") {
      return { kind: "help" };
    }

    if (arg === "--dry-run") {
      dryRun = true;
      continue;
    }

    if (!arg.startsWith("--")) {
      throw new Error(`Unexpected argument: ${arg}`);
    }

    const key = arg.slice(2);
    const value = args[index + 1];

    if (!value || value.startsWith("--")) {
      throw new Error(`Missing value for --${key}`);
    }

    if (key === "metadata") {
      const separator = value.indexOf("=");
      if (separator === -1) {
        throw new Error("--metadata must use key=value.");
      }
      metadata[value.slice(0, separator)] = value.slice(separator + 1);
    } else {
      values.set(key, value);
    }

    index += 1;
  }

  const apiKey = values.get("api-key") ?? process.env.RAWTREE_API_KEY ?? process.env.RAWTREE_TOKEN;
  const kernel = values.get("kernel");
  const rootfs = values.get("rootfs");

  if (!apiKey && !dryRun) {
    throw new Error("Missing --api-key or RAWTREE_API_KEY.");
  }

  if (!kernel) {
    throw new Error("Missing --kernel.");
  }

  if (!rootfs) {
    throw new Error("Missing --rootfs.");
  }

  const options: CliOptions = {
    apiKey: apiKey ?? "dry-run-api-key-not-used",
    baseUrl: values.get("base-url") ?? process.env.RAWTREE_BASE_URL ?? DEFAULT_BASE_URL,
    dryRun,
    firecracker: values.get("firecracker") ?? "firecracker",
    hypervisorSampleIntervalMs: numberOption(
      values,
      "hypervisor-sample-interval-ms",
      DEFAULT_HYPERVISOR_SAMPLE_INTERVAL_MS,
    ),
    kernel,
    memMiB: numberOption(values, "mem-mib", 512),
    metricsFlushIntervalMs: nonNegativeNumberOption(
      values,
      "metrics-flush-interval-ms",
      DEFAULT_METRICS_FLUSH_INTERVAL_MS,
    ),
    metadata,
    provider: values.get("provider") ?? DEFAULT_PROVIDER,
    rootfs,
    runTimeoutMs: numberOption(values, "run-timeout-ms", DEFAULT_RUN_TIMEOUT_MS),
    sandboxId: values.get("sandbox-id") ?? `sbx_${randomUUID()}`,
    table: values.get("table") ?? process.env.RAWTREE_SANDBOX_TABLE ?? DEFAULT_TABLE,
    vcpuCount: numberOption(values, "vcpu-count", 1),
  };

  const cgroupPath = values.get("cgroup-path");
  if (cgroupPath) {
    options.cgroupPath = cgroupPath;
  }

  const bootArgs = values.get("boot-args");
  if (bootArgs) {
    options.bootArgs = bootArgs;
  }

  const guestMac = values.get("guest-mac");
  if (guestMac) {
    options.guestMac = guestMac;
  }

  const tap = values.get("tap");
  if (tap) {
    options.tap = tap;
  }

  return {
    kind: "run",
    options,
  };
}

function numberOption(values: Map<string, string>, key: string, fallback: number): number {
  const value = values.get(key);
  if (!value) {
    return fallback;
  }

  const parsed = Number(value);
  if (!Number.isInteger(parsed) || parsed <= 0) {
    throw new Error(`--${key} must be a positive integer.`);
  }

  return parsed;
}

function nonNegativeNumberOption(values: Map<string, string>, key: string, fallback: number): number {
  const value = values.get(key);
  if (!value) {
    return fallback;
  }

  const parsed = Number(value);
  if (!Number.isInteger(parsed) || parsed < 0) {
    throw new Error(`--${key} must be a non-negative integer.`);
  }

  return parsed;
}

function startMetricsFlusher(
  fc: ReturnType<typeof firecrackerClient>,
  intervalMs: number,
): (() => Promise<void>) | undefined {
  if (intervalMs <= 0) {
    return undefined;
  }

  let flushing: Promise<void> | undefined;

  const interval = setInterval(() => {
    if (flushing) {
      return;
    }

    flushing = fc.put("/actions", { action_type: "FlushMetrics" }).catch((error) => {
      console.error("Firecracker metrics flush failed:", error);
    }).finally(() => {
      flushing = undefined;
    });
  }, intervalMs);
  interval.unref();

  return async () => {
    clearInterval(interval);
    await flushing;
  };
}

async function moveProcessToCgroup(pid: number, cgroupPath: string): Promise<string> {
  const relativePath = normalizeCgroupPath(cgroupPath);
  const cgroupDir = path.join("/sys/fs/cgroup", relativePath);

  await fs.mkdir(cgroupDir, { recursive: true });
  await fs.writeFile(path.join(cgroupDir, "cgroup.procs"), `${pid}\n`);

  return cgroupDir;
}

function normalizeCgroupPath(cgroupPath: string): string {
  const normalized = path.posix.normalize(`/${cgroupPath}`).replace(/^\/+/, "");
  if (!normalized || normalized === "." || normalized.startsWith("..") || normalized.includes("/../")) {
    throw new Error("--cgroup-path must be a relative cgroup v2 path.");
  }

  return normalized;
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function errorFields(error: unknown): Record<string, string> {
  if (error instanceof Error) {
    return {
      error_message: error.message,
      error_name: error.name,
      error_stack: error.stack ?? "",
    };
  }

  return {
    error_message: String(error),
    error_name: "UnknownError",
  };
}

function usage(): string {
  return [
    "Usage:",
    "  sudo -E npm run start -- \\",
    "    --firecracker /usr/local/bin/firecracker \\",
    "    --kernel /var/lib/firecracker/vmlinux \\",
    "    --rootfs /var/lib/firecracker/rootfs.ext4",
    "",
    "Dry run:",
    "  npm run dry-run",
    "",
    "Options:",
    "  --api-key <key>          RawTree API key; host collector only",
    "  --base-url <url>         RawTree API base URL",
    "  --boot-args <args>       Kernel boot args, default: " + DEFAULT_BOOT_ARGS,
    "  --cgroup-path <path>     Optional cgroup v2 path for the Firecracker process",
    "  --dry-run                Print the provider integration plan without starting Firecracker",
    "  --guest-mac <mac>        Guest MAC for optional TAP device",
    "  --hypervisor-sample-interval-ms <n>",
    "                           Host process/cgroup sample interval, default 1000",
    "  --mem-mib <n>            Memory in MiB, default 512",
    "  --metrics-flush-interval-ms <n>",
    "                           Periodic Firecracker FlushMetrics interval; 0 disables it",
    "  --metadata <key=value>   Provider metadata; can be passed multiple times",
    "  --provider <name>        Provider name",
    "  --run-timeout-ms <n>     Demo stop timeout before FlushMetrics and SIGTERM, default 30000",
    "  --sandbox-id <id>        Existing internal sandbox id",
    "  --table <name>           RawTree table, default sandbox_events",
    "  --tap <name>             Optional TAP device name",
    "  --vcpu-count <n>         vCPU count, default 1",
  ].join("\n");
}

await main();
