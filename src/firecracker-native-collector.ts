import fs from "node:fs/promises";

import type { HostCollector } from "./host-collector.js";
import type { JsonObject, RawTreeEvent, RuntimePaths } from "./types.js";

export async function prepareFirecrackerOutputFiles(paths: RuntimePaths): Promise<void> {
  await Promise.all([
    fs.writeFile(paths.firecrackerLogPath, ""),
    fs.writeFile(paths.firecrackerMetricsPath, ""),
  ]);
}

export async function emitFirecrackerNativeEvents(
  paths: RuntimePaths,
  collector: HostCollector,
): Promise<void> {
  await emitFirecrackerLogs(paths.firecrackerLogPath, collector);
  await emitFirecrackerMetrics(paths.firecrackerMetricsPath, collector);
}

async function emitFirecrackerLogs(logPath: string, collector: HostCollector): Promise<void> {
  const content = await fs.readFile(logPath, "utf8").catch(() => "");
  const lines = content.split(/\r?\n/).map((line) => line.trim()).filter(Boolean);

  for (const line of lines) {
    const event: RawTreeEvent = {
      event_type: "sandbox.firecracker.vmm.log",
      firecracker: {
        log: {
          line,
        },
      },
      source: "firecracker_vmm_logger",
      status: "success",
    };
    const sampledAt = sampledAtFromLogLine(line);
    if (sampledAt) {
      event.sampled_at = sampledAt;
    }
    await collector.record(event);
  }
}

async function emitFirecrackerMetrics(metricsPath: string, collector: HostCollector): Promise<void> {
  const content = await fs.readFile(metricsPath, "utf8").catch(() => "");
  const lines = content.split(/\r?\n/).map((line) => line.trim()).filter(Boolean);

  for (const line of lines) {
    const metrics = parseJsonObject(line);

    const event: RawTreeEvent = {
      event_type: "sandbox.firecracker.vmm.metrics",
      firecracker: {
        metrics: metrics ?? {
          raw_line: line,
        },
      },
      source: "firecracker_vmm_metrics",
      status: "success",
    };
    const sampledAt = sampledAtFromMetrics(metrics);
    if (sampledAt) {
      event.sampled_at = sampledAt;
    }
    await collector.record(event);
  }
}

function parseJsonObject(line: string): JsonObject | undefined {
  try {
    const value: unknown = JSON.parse(line);
    if (typeof value === "object" && value !== null && !Array.isArray(value)) {
      return value as JsonObject;
    }
  } catch {
    return undefined;
  }

  return undefined;
}

function sampledAtFromMetrics(metrics: JsonObject | undefined): string | undefined {
  if (!metrics) {
    return undefined;
  }

  const timestampMs = metrics.utc_timestamp_ms;
  if (typeof timestampMs !== "number" || !Number.isFinite(timestampMs)) {
    return undefined;
  }

  return new Date(timestampMs).toISOString();
}

function sampledAtFromLogLine(line: string): string | undefined {
  const match = line.match(
    /^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?/,
  );
  if (!match) {
    return undefined;
  }

  const [, timestamp, fraction = ""] = match;
  const milliseconds = fraction.slice(0, 3).padEnd(3, "0");
  return `${timestamp}.${milliseconds}Z`;
}
