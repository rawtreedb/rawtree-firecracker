import fs from "node:fs/promises";

import type { HostCollector } from "./host-collector.js";
import type { JsonObject } from "./types.js";

export type HypervisorSampler = {
  sample(): Promise<void>;
  stop(): Promise<void>;
};

type HypervisorSamplerOptions = {
  collector: HostCollector;
  intervalMs: number;
  pid: number;
};

const LINUX_PAGE_SIZE_BYTES = 4096;

export function startHypervisorSampler(options: HypervisorSamplerOptions): HypervisorSampler {
  let stopped = false;
  let sampling: Promise<void> | undefined;

  const interval = setInterval(() => {
    void sample();
  }, options.intervalMs);
  interval.unref();

  void sample();

  async function sample(): Promise<void> {
    if (stopped) {
      return;
    }

    if (sampling) {
      return sampling;
    }

    sampling = emitSample(options).finally(() => {
      sampling = undefined;
    });

    return sampling;
  }

  return {
    sample,
    async stop() {
      stopped = true;
      clearInterval(interval);
      await sampling;
    },
  };
}

async function emitSample(options: HypervisorSamplerOptions): Promise<void> {
  try {
    const sampledAt = new Date().toISOString();
    const metrics = await readHypervisorMetrics(options.pid);
    await options.collector.record({
      event_type: "sandbox.hypervisor.sample",
      hypervisor: metrics,
      sampled_at: sampledAt,
      source: "host_hypervisor_sampler",
      status: "success",
    });
  } catch (error) {
    await options.collector.record({
      event_type: "sandbox.hypervisor.sample.failed",
      source: "host_hypervisor_sampler",
      status: "error",
      ...errorFields(error),
    });
  }
}

async function readHypervisorMetrics(pid: number): Promise<JsonObject> {
  const [stat, status, io, fdCount, cgroup] = await Promise.all([
    readProcStat(pid),
    readProcStatus(pid),
    readProcIo(pid),
    readFdCount(pid),
    readCgroupMetrics(pid),
  ]);

  return {
    pid,
    process: {
      fd_count: fdCount,
      io,
      stat,
      status,
    },
    cgroup,
  };
}

async function readProcStat(pid: number): Promise<JsonObject> {
  const stat = await fs.readFile(`/proc/${pid}/stat`, "utf8");
  const parsed = parseProcStat(stat);
  const cpuUserTicks = numberFromString(parsed.fields[11]);
  const cpuSystemTicks = numberFromString(parsed.fields[12]);
  const rssPages = numberFromString(parsed.fields[21]);

  return {
    command: parsed.command,
    cpu_system_ticks: cpuSystemTicks,
    cpu_total_ticks: cpuUserTicks + cpuSystemTicks,
    cpu_user_ticks: cpuUserTicks,
    major_faults: numberFromString(parsed.fields[9]),
    minor_faults: numberFromString(parsed.fields[7]),
    parent_pid: numberFromString(parsed.fields[1]),
    rss_bytes: rssPages * LINUX_PAGE_SIZE_BYTES,
    rss_pages: rssPages,
    start_time_ticks: numberFromString(parsed.fields[19]),
    state: parsed.state,
    threads: numberFromString(parsed.fields[17]),
    vsize_bytes: numberFromString(parsed.fields[20]),
  };
}

function parseProcStat(value: string): { command: string; fields: string[]; state: string } {
  const open = value.indexOf("(");
  const close = value.lastIndexOf(")");

  if (open === -1 || close === -1 || close <= open) {
    throw new Error("Unexpected /proc/<pid>/stat format.");
  }

  const command = value.slice(open + 1, close);
  const fields = value.slice(close + 2).trim().split(/\s+/);

  return {
    command,
    fields,
    state: fields[0] ?? "",
  };
}

async function readProcStatus(pid: number): Promise<JsonObject> {
  const status = await fs.readFile(`/proc/${pid}/status`, "utf8");
  const fields = parseKeyValueLines(status);

  return {
    nonvoluntary_context_switches: numberFromString(fields.nonvoluntary_ctxt_switches),
    threads: numberFromString(fields.Threads),
    vm_hwm_bytes: kibibytesToBytes(fields.VmHWM),
    vm_peak_bytes: kibibytesToBytes(fields.VmPeak),
    vm_rss_bytes: kibibytesToBytes(fields.VmRSS),
    vm_size_bytes: kibibytesToBytes(fields.VmSize),
    voluntary_context_switches: numberFromString(fields.voluntary_ctxt_switches),
  };
}

async function readProcIo(pid: number): Promise<JsonObject> {
  const io = await fs.readFile(`/proc/${pid}/io`, "utf8").catch(() => "");
  const fields = parseKeyValueLines(io);

  return {
    cancelled_write_bytes: numberFromString(fields.cancelled_write_bytes),
    read_bytes: numberFromString(fields.read_bytes),
    read_chars: numberFromString(fields.rchar),
    sys_read_count: numberFromString(fields.syscr),
    sys_write_count: numberFromString(fields.syscw),
    write_bytes: numberFromString(fields.write_bytes),
    write_chars: numberFromString(fields.wchar),
  };
}

async function readFdCount(pid: number): Promise<number> {
  const entries = await fs.readdir(`/proc/${pid}/fd`).catch(() => []);
  return entries.length;
}

async function readCgroupMetrics(pid: number): Promise<JsonObject> {
  const cgroupPath = await readUnifiedCgroupPath(pid);
  if (!cgroupPath) {
    return {};
  }

  const cgroupRoot = `/sys/fs/cgroup${cgroupPath}`;
  const [cpuStat, memoryCurrent, memoryPeak] = await Promise.all([
    readCgroupCpuStat(cgroupRoot),
    readNumberFile(`${cgroupRoot}/memory.current`),
    readNumberFile(`${cgroupRoot}/memory.peak`),
  ]);

  return {
    cpu_stat: cpuStat,
    memory_current_bytes: memoryCurrent,
    memory_peak_bytes: memoryPeak,
    path: cgroupPath,
  };
}

async function readUnifiedCgroupPath(pid: number): Promise<string | undefined> {
  const content = await fs.readFile(`/proc/${pid}/cgroup`, "utf8").catch(() => "");

  for (const line of content.split("\n")) {
    const [hierarchy, controllers, cgroupPath] = line.split(":");
    if (hierarchy === "0" && controllers === "" && cgroupPath) {
      return cgroupPath;
    }
  }

  return undefined;
}

async function readCgroupCpuStat(cgroupRoot: string): Promise<JsonObject> {
  const content = await fs.readFile(`${cgroupRoot}/cpu.stat`, "utf8").catch(() => "");
  const fields = parseSpaceSeparatedKeyValueLines(content);

  return {
    nr_periods: numberFromString(fields.nr_periods),
    nr_throttled: numberFromString(fields.nr_throttled),
    system_usec: numberFromString(fields.system_usec),
    throttled_usec: numberFromString(fields.throttled_usec),
    usage_usec: numberFromString(fields.usage_usec),
    user_usec: numberFromString(fields.user_usec),
  };
}

async function readNumberFile(filePath: string): Promise<number> {
  const content = await fs.readFile(filePath, "utf8").catch(() => "");
  return numberFromString(content.trim());
}

function parseKeyValueLines(content: string): Record<string, string> {
  const result: Record<string, string> = {};

  for (const line of content.split("\n")) {
    const separator = line.indexOf(":");
    if (separator === -1) {
      continue;
    }

    result[line.slice(0, separator).trim()] = line.slice(separator + 1).trim();
  }

  return result;
}

function parseSpaceSeparatedKeyValueLines(content: string): Record<string, string> {
  const result: Record<string, string> = {};

  for (const line of content.split("\n")) {
    const [key, value] = line.trim().split(/\s+/, 2);
    if (key && value) {
      result[key] = value;
    }
  }

  return result;
}

function kibibytesToBytes(value: string | undefined): number {
  return numberFromString(value) * 1024;
}

function numberFromString(value: string | undefined): number {
  if (!value) {
    return 0;
  }

  const match = value.match(/-?\d+/);
  if (!match) {
    return 0;
  }

  const parsed = Number(match[0]);
  return Number.isFinite(parsed) ? parsed : 0;
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
