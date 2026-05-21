import fs from "node:fs/promises";

import type { HostCollector } from "./host-collector.js";
import type { RuntimePaths } from "./types.js";

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
    await collector.record({
      event_type: "sandbox.firecracker.vmm.log",
      source: "firecracker_vmm_logger",
      status: "success",
      firecracker_log_line: line,
    });
  }
}

async function emitFirecrackerMetrics(metricsPath: string, collector: HostCollector): Promise<void> {
  const content = await fs.readFile(metricsPath, "utf8").catch(() => "");
  const lines = content.split(/\r?\n/).map((line) => line.trim()).filter(Boolean);

  for (const line of lines) {
    await collector.record({
      event_type: "sandbox.firecracker.vmm.metrics",
      source: "firecracker_vmm_metrics",
      status: "success",
      firecracker_metrics_json: line,
    });
  }
}
