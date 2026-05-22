#!/usr/bin/env node
import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";

const repoRoot = path.resolve(new URL("..", import.meta.url).pathname);
const runId = process.argv[2] ?? process.env.RAWTREE_RUN_ID;
const apiKey = process.env.RAWTREE_API_KEY ?? process.env.RAWTREE_TOKEN;
const baseUrl = process.env.RAWTREE_BASE_URL ?? "https://api.rawtree.com";

if (!runId) {
  throw new Error("Usage: RAWTREE_API_KEY=... node scripts/generate-rich-report.mjs <RUN_ID>");
}

if (!apiKey) {
  throw new Error("Set RAWTREE_API_KEY before generating a report.");
}

const queries = {
  eventCounts: "sql/00_event_counts.sql",
  timeline: "sql/01_event_timeline.sql",
  hypervisor: "sql/02_hypervisor_cpu_memory.sql",
  io: "sql/03_firecracker_io_metrics.sql",
  logs: "sql/04_firecracker_logs.sql",
  summary: "sql/05_run_summary.sql",
};

const results = {};
for (const [name, filePath] of Object.entries(queries)) {
  const sqlTemplate = await readFile(path.join(repoRoot, filePath), "utf8");
  const sql = sqlTemplate.replaceAll("<RUN_ID>", runId);
  results[name] = await queryRawTree(sql);
}

const payload = {
  generatedAt: new Date().toISOString(),
  runId,
  results,
};

const reportPath = path.join(repoRoot, "reports", `rich-run-${shortRunId(runId)}.html`);
await mkdir(path.dirname(reportPath), { recursive: true });
await writeFile(reportPath, renderHtml(payload), "utf8");
console.log(reportPath);

async function queryRawTree(sql) {
  const response = await fetch(`${baseUrl.replace(/\/+$/, "")}/v1/query`, {
    body: JSON.stringify({ sql }),
    headers: {
      Authorization: `Bearer ${apiKey}`,
      "Content-Type": "application/json",
    },
    method: "POST",
  });

  const text = await response.text();
  let body;
  try {
    body = JSON.parse(text);
  } catch {
    throw new Error(`RawTree returned non-JSON response (${response.status}): ${text.slice(0, 500)}`);
  }

  if (!response.ok || body.error) {
    throw new Error(`RawTree query failed (${response.status}): ${JSON.stringify(body).slice(0, 1000)}`);
  }

  return {
    data: body.data ?? [],
    rows: body.rows ?? 0,
  };
}

function shortRunId(value) {
  return value.replace(/^rt_firecracker_sandbox_run_/, "").slice(0, 8);
}

function safeJson(value) {
  return JSON.stringify(value).replaceAll("<", "\\u003c");
}

function renderHtml(data) {
  return `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <title>RawTree Firecracker Rich Run</title>
    <style>
      :root {
        --bg: #f6f7fb;
        --panel: #ffffff;
        --text: #172033;
        --muted: #667085;
        --border: #d9e0ea;
        --grid: #e4e8f0;
        --blue: #2563eb;
        --green: #059669;
        --orange: #d97706;
        --pink: #db2777;
        --purple: #7c3aed;
        --cyan: #0891b2;
        --red: #dc2626;
        --slate: #475569;
      }

      * {
        box-sizing: border-box;
      }

      body {
        margin: 0;
        background: var(--bg);
        color: var(--text);
        font-family:
          Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont,
          "Segoe UI", sans-serif;
      }

      main {
        max-width: 1220px;
        margin: 0 auto;
        padding: 30px 20px 56px;
      }

      header {
        margin-bottom: 22px;
      }

      h1 {
        margin: 0;
        font-size: 28px;
        line-height: 1.2;
      }

      h2 {
        margin: 0;
        font-size: 18px;
      }

      p {
        color: var(--muted);
        line-height: 1.5;
      }

      code,
      pre {
        font-family: "SFMono-Regular", Consolas, "Liberation Mono", Menlo, monospace;
      }

      .meta {
        display: flex;
        flex-wrap: wrap;
        gap: 8px;
        margin-top: 12px;
      }

      .chip {
        border: 1px solid var(--border);
        border-radius: 6px;
        background: var(--panel);
        padding: 6px 9px;
        color: var(--muted);
        font-size: 13px;
      }

      .stats {
        display: grid;
        grid-template-columns: repeat(5, minmax(0, 1fr));
        gap: 12px;
        margin: 20px 0;
      }

      .stat,
      .chart,
      .logs,
      details {
        border: 1px solid var(--border);
        border-radius: 8px;
        background: var(--panel);
      }

      .stat {
        padding: 14px;
        min-width: 0;
      }

      .label {
        color: var(--muted);
        font-size: 12px;
        text-transform: uppercase;
      }

      .value {
        margin-top: 6px;
        overflow-wrap: anywhere;
        font-size: 22px;
        font-weight: 750;
      }

      .grid {
        display: grid;
        grid-template-columns: repeat(2, minmax(0, 1fr));
        gap: 16px;
      }

      .chart,
      .logs {
        margin-top: 16px;
        padding: 18px;
      }

      .wide {
        grid-column: 1 / -1;
      }

      .chart p {
        margin: 6px 0 12px;
        font-size: 14px;
      }

      svg {
        display: block;
        width: 100%;
        height: auto;
      }

      .axis {
        fill: var(--muted);
        font-size: 12px;
      }

      .grid-line {
        stroke: var(--grid);
        stroke-width: 1;
      }

      .series {
        fill: none;
        stroke-width: 3;
        stroke-linecap: round;
        stroke-linejoin: round;
      }

      .point {
        stroke: var(--panel);
        stroke-width: 2;
      }

      .legend {
        display: flex;
        flex-wrap: wrap;
        gap: 14px;
        margin-top: 12px;
        color: var(--muted);
        font-size: 13px;
      }

      .legend span {
        display: inline-flex;
        align-items: center;
        gap: 6px;
      }

      .swatch {
        width: 10px;
        height: 10px;
        border-radius: 2px;
      }

      .logs-list {
        display: grid;
        gap: 8px;
        margin-top: 12px;
      }

      .log-row {
        display: grid;
        grid-template-columns: 170px minmax(0, 1fr);
        gap: 10px;
        border: 1px solid var(--grid);
        border-radius: 6px;
        padding: 9px;
        font-size: 12px;
      }

      .log-time {
        color: var(--muted);
      }

      .log-line {
        overflow-wrap: anywhere;
      }

      details {
        margin-top: 16px;
        padding: 14px 16px;
      }

      summary {
        cursor: pointer;
        font-weight: 700;
      }

      pre {
        overflow: auto;
        margin: 14px 0 0;
        padding: 12px;
        border-radius: 6px;
        background: #111827;
        color: #eef2ff;
        font-size: 12px;
        line-height: 1.5;
      }

      @media (max-width: 980px) {
        .stats {
          grid-template-columns: repeat(2, minmax(0, 1fr));
        }

        .grid {
          grid-template-columns: 1fr;
        }
      }

      @media (max-width: 680px) {
        main {
          padding: 22px 14px 40px;
        }

        .stats {
          grid-template-columns: 1fr;
        }

        .log-row {
          grid-template-columns: 1fr;
        }
      }
    </style>
  </head>
  <body>
    <main>
      <header>
        <h1>RawTree Firecracker Rich Run</h1>
        <div class="meta">
          <span class="chip">run_id: ${escapeHtml(data.runId)}</span>
          <span class="chip">generated: ${escapeHtml(data.generatedAt)}</span>
          <span class="chip">source: sandbox_events</span>
        </div>
      </header>

      <section class="stats" id="summary"></section>

      <section class="grid">
        <article class="chart">
          <h2>Event Mix</h2>
          <p>How many rows each event type produced for this run.</p>
          <div id="event-counts"></div>
          <div id="event-counts-legend" class="legend"></div>
        </article>

        <article class="chart">
          <h2>Event Timeline</h2>
          <p>Stacked event volume by second. The periodic bars are host samples and Firecracker metric flushes.</p>
          <div id="event-timeline"></div>
          <div id="timeline-legend" class="legend"></div>
        </article>

        <article class="chart">
          <h2>CPU</h2>
          <p>CPU percent from host process ticks and cgroup CPU usage deltas. This run used two vCPUs, so values can approach 200%.</p>
          <div id="cpu-chart"></div>
          <div class="legend">
            <span><i class="swatch" style="background: var(--orange)"></i>Firecracker process CPU %</span>
            <span><i class="swatch" style="background: var(--purple)"></i>Sandbox cgroup CPU %</span>
          </div>
        </article>

        <article class="chart">
          <h2>Memory</h2>
          <p>Firecracker process RSS compared with memory charged to the sandbox cgroup.</p>
          <div id="memory-chart"></div>
          <div class="legend">
            <span><i class="swatch" style="background: var(--blue)"></i>Process RSS MiB</span>
            <span><i class="swatch" style="background: var(--green)"></i>Sandbox cgroup MiB</span>
          </div>
        </article>

        <article class="chart wide">
          <h2>Rootfs IO Per Flush</h2>
          <p>Firecracker metrics are emitted per flush, so this chart shows per-sample read/write MiB instead of cumulative deltas.</p>
          <div id="io-chart"></div>
          <div class="legend">
            <span><i class="swatch" style="background: var(--cyan)"></i>Rootfs read MiB</span>
            <span><i class="swatch" style="background: var(--pink)"></i>Rootfs write MiB</span>
          </div>
        </article>

        <article class="chart wide">
          <h2>vCPU Exits And UART Writes</h2>
          <p>Useful low-level VMM counters for understanding guest/device activity during the workload.</p>
          <div id="vmm-chart"></div>
          <div class="legend">
            <span><i class="swatch" style="background: var(--red)"></i>vCPU exits</span>
            <span><i class="swatch" style="background: var(--slate)"></i>UART writes</span>
          </div>
        </article>
      </section>

      <section class="logs">
        <h2>Firecracker Logs</h2>
        <p>First 16 VMM log lines from the run.</p>
        <div id="logs" class="logs-list"></div>
      </section>

      <details>
        <summary>Embedded Query Results</summary>
        <pre id="raw-json"></pre>
      </details>
    </main>

    <script>
      const report = ${safeJson(data)};
      const colors = {
        blue: "#2563eb",
        green: "#059669",
        orange: "#d97706",
        pink: "#db2777",
        purple: "#7c3aed",
        cyan: "#0891b2",
        red: "#dc2626",
        slate: "#475569",
      };
      const eventColors = [
        colors.blue,
        colors.green,
        colors.orange,
        colors.pink,
        colors.purple,
        colors.cyan,
        colors.red,
        colors.slate,
      ];

      function html(value) {
        return String(value)
          .replaceAll("&", "&amp;")
          .replaceAll("<", "&lt;")
          .replaceAll(">", "&gt;")
          .replaceAll('"', "&quot;");
      }

      function toMs(value) {
        if (!value) return 0;
        return Date.parse(String(value).replace(" ", "T") + "Z");
      }

      function secondsLabel(ms, minMs) {
        return ((ms - minMs) / 1000).toFixed(0) + "s";
      }

      function fmt(value, digits = 1) {
        if (value === null || value === undefined || Number.isNaN(Number(value))) return "-";
        return Number(value).toLocaleString(undefined, { maximumFractionDigits: digits });
      }

      function fmtBytes(value) {
        const bytes = Number(value || 0);
        if (bytes >= 1073741824) return fmt(bytes / 1073741824, 2) + " GiB";
        if (bytes >= 1048576) return fmt(bytes / 1048576, 2) + " MiB";
        if (bytes >= 1024) return fmt(bytes / 1024, 2) + " KiB";
        return fmt(bytes, 0) + " B";
      }

      function niceMax(value) {
        if (!Number.isFinite(value) || value <= 0) return 1;
        const exponent = Math.floor(Math.log10(value));
        const base = 10 ** exponent;
        return Math.ceil((value * 1.08) / base) * base;
      }

      function renderSummary() {
        const row = report.results.summary.data[0] ?? {};
        const stats = [
          ["Duration", fmt(row.duration_seconds, 0) + "s"],
          ["Total events", fmt(row.total_events, 0)],
          ["Hypervisor samples", fmt(row.hypervisor_samples, 0)],
          ["Metric samples", fmt(row.firecracker_metric_samples, 0)],
          ["Peak cgroup memory", fmt(row.peak_sandbox_cgroup_memory_mib, 2) + " MiB"],
          ["Peak process RSS", fmt(row.peak_process_rss_mib, 2) + " MiB"],
          ["Rootfs read", fmtBytes(row.rootfs_read_bytes)],
          ["Rootfs write", fmtBytes(row.rootfs_write_bytes)],
          ["Started", row.started_at ?? "-"],
          ["Finished", row.finished_at ?? "-"],
        ];

        document.getElementById("summary").innerHTML = stats
          .map(([label, value]) => '<div class="stat"><div class="label">' + html(label) + '</div><div class="value">' + html(value) + '</div></div>')
          .join("");
      }

      function chartFrame(width, height, body) {
        return '<svg viewBox="0 0 ' + width + ' ' + height + '" role="img">' + body + '</svg>';
      }

      function renderHorizontalBars(targetId, rows) {
        const width = 540;
        const rowHeight = 36;
        const margin = { top: 12, right: 24, bottom: 20, left: 238 };
        const height = margin.top + margin.bottom + rows.length * rowHeight;
        const maxValue = Math.max(...rows.map((row) => Number(row.events || 0)), 1);
        const innerWidth = width - margin.left - margin.right;
        const body = rows.map((row, index) => {
          const value = Number(row.events || 0);
          const barWidth = (value / maxValue) * innerWidth;
          const y = margin.top + index * rowHeight + 7;
          const color = eventColors[index % eventColors.length];
          return [
            '<text class="axis" x="' + (margin.left - 10) + '" y="' + (y + 16) + '" text-anchor="end">' + html(row.event_type) + '</text>',
            '<rect x="' + margin.left + '" y="' + y + '" width="' + barWidth + '" height="20" rx="3" fill="' + color + '"><title>' + html(row.event_type) + ': ' + value + '</title></rect>',
            '<text class="axis" x="' + (margin.left + barWidth + 8) + '" y="' + (y + 15) + '">' + value + '</text>',
          ].join("");
        }).join("");
        document.getElementById(targetId).innerHTML = chartFrame(width, height, body);
      }

      function renderStackedTimeline(targetId, legendId, rows) {
        const width = 540;
        const height = 285;
        const margin = { top: 14, right: 18, bottom: 38, left: 42 };
        const seconds = [...new Set(rows.map((row) => row.second))].sort();
        const eventTypes = [...new Set(rows.map((row) => row.event_type))].sort();
        const grouped = new Map(seconds.map((second) => [second, Object.fromEntries(eventTypes.map((type) => [type, 0]))]));
        for (const row of rows) grouped.get(row.second)[row.event_type] = Number(row.events || 0);
        const maxTotal = Math.max(...seconds.map((second) => eventTypes.reduce((sum, type) => sum + grouped.get(second)[type], 0)), 1);
        const innerWidth = width - margin.left - margin.right;
        const innerHeight = height - margin.top - margin.bottom;
        const barGap = 2;
        const barWidth = Math.max(2, innerWidth / seconds.length - barGap);
        const yScale = (value) => margin.top + innerHeight - (value / maxTotal) * innerHeight;
        const minMs = toMs(seconds[0]);
        const yTicks = Array.from({ length: 4 }, (_, index) => (maxTotal / 3) * index);

        let body = yTicks.map((tick) => {
          const y = yScale(tick);
          return '<line class="grid-line" x1="' + margin.left + '" x2="' + (width - margin.right) + '" y1="' + y + '" y2="' + y + '"></line>'
            + '<text class="axis" x="' + (margin.left - 8) + '" y="' + (y + 4) + '" text-anchor="end">' + fmt(tick, 0) + '</text>';
        }).join("");

        seconds.forEach((second, index) => {
          const x = margin.left + index * (barWidth + barGap);
          let stack = 0;
          eventTypes.forEach((type, typeIndex) => {
            const value = grouped.get(second)[type];
            if (!value) return;
            const y0 = yScale(stack);
            const y1 = yScale(stack + value);
            const h = Math.max(1, y0 - y1);
            body += '<rect x="' + x + '" y="' + y1 + '" width="' + barWidth + '" height="' + h + '" fill="' + eventColors[typeIndex % eventColors.length] + '"><title>' + html(second + " " + type + ": " + value) + '</title></rect>';
            stack += value;
          });
        });

        [0, Math.floor(seconds.length / 2), seconds.length - 1].forEach((index) => {
          const x = margin.left + index * (barWidth + barGap);
          body += '<text class="axis" x="' + x + '" y="' + (height - 14) + '" text-anchor="middle">' + secondsLabel(toMs(seconds[index]), minMs) + '</text>';
        });

        document.getElementById(targetId).innerHTML = chartFrame(width, height, body);
        document.getElementById(legendId).innerHTML = eventTypes.map((type, index) =>
          '<span><i class="swatch" style="background:' + eventColors[index % eventColors.length] + '"></i>' + html(type.replace("sandbox.", "")) + '</span>'
        ).join("");
      }

      function renderLineChart(targetId, rows, series, unit = "") {
        const width = 920;
        const height = 320;
        const margin = { top: 18, right: 26, bottom: 44, left: 66 };
        const points = rows.map((row) => ({ ...row, __ms: toMs(row.ts) })).filter((row) => row.__ms);
        const minMs = Math.min(...points.map((row) => row.__ms));
        const maxMs = Math.max(...points.map((row) => row.__ms));
        const values = series.flatMap(({ key }) => points.map((row) => Number(row[key])).filter(Number.isFinite));
        const yMax = niceMax(Math.max(...values, 1));
        const innerWidth = width - margin.left - margin.right;
        const innerHeight = height - margin.top - margin.bottom;
        const xScale = (ms) => margin.left + ((ms - minMs) / Math.max(maxMs - minMs, 1)) * innerWidth;
        const yScale = (value) => margin.top + innerHeight - (value / yMax) * innerHeight;
        const yTicks = Array.from({ length: 5 }, (_, index) => (yMax / 4) * index);
        const xTicks = Array.from({ length: 5 }, (_, index) => minMs + ((maxMs - minMs) / 4) * index);

        let body = yTicks.map((tick) => {
          const y = yScale(tick);
          return '<line class="grid-line" x1="' + margin.left + '" x2="' + (width - margin.right) + '" y1="' + y + '" y2="' + y + '"></line>'
            + '<text class="axis" x="' + (margin.left - 10) + '" y="' + (y + 4) + '" text-anchor="end">' + fmt(tick) + unit + '</text>';
        }).join("");

        body += xTicks.map((tick) => {
          const x = xScale(tick);
          return '<line class="grid-line" x1="' + x + '" x2="' + x + '" y1="' + margin.top + '" y2="' + (height - margin.bottom) + '"></line>'
            + '<text class="axis" x="' + x + '" y="' + (height - 15) + '" text-anchor="middle">' + secondsLabel(tick, minMs) + '</text>';
        }).join("");

        series.forEach(({ key, color }) => {
          const usable = points.filter((row) => Number.isFinite(Number(row[key])));
          const path = usable.map((row, index) => (index === 0 ? "M" : "L") + xScale(row.__ms).toFixed(2) + "," + yScale(Number(row[key])).toFixed(2)).join(" ");
          body += '<path class="series" d="' + path + '" stroke="' + color + '"></path>';
          body += usable.map((row) => '<circle class="point" cx="' + xScale(row.__ms) + '" cy="' + yScale(Number(row[key])) + '" r="3.5" fill="' + color + '"><title>' + html(row.ts + ": " + fmt(row[key]) + unit) + '</title></circle>').join("");
        });

        document.getElementById(targetId).innerHTML = chartFrame(width, height, body);
      }

      function renderGroupedBars(targetId, rows, series, unit = "") {
        const width = 1040;
        const height = 330;
        const margin = { top: 18, right: 24, bottom: 48, left: 72 };
        const points = rows.map((row) => ({ ...row, __ms: toMs(row.ts) })).filter((row) => row.__ms);
        const minMs = Math.min(...points.map((row) => row.__ms));
        const values = series.flatMap(({ key }) => points.map((row) => Number(row[key])).filter(Number.isFinite));
        const yMax = niceMax(Math.max(...values, 1));
        const innerWidth = width - margin.left - margin.right;
        const innerHeight = height - margin.top - margin.bottom;
        const groupWidth = innerWidth / points.length;
        const barWidth = Math.max(2, (groupWidth - 4) / series.length);
        const yScale = (value) => margin.top + innerHeight - (value / yMax) * innerHeight;
        const yTicks = Array.from({ length: 5 }, (_, index) => (yMax / 4) * index);

        let body = yTicks.map((tick) => {
          const y = yScale(tick);
          return '<line class="grid-line" x1="' + margin.left + '" x2="' + (width - margin.right) + '" y1="' + y + '" y2="' + y + '"></line>'
            + '<text class="axis" x="' + (margin.left - 10) + '" y="' + (y + 4) + '" text-anchor="end">' + fmt(tick) + unit + '</text>';
        }).join("");

        points.forEach((row, index) => {
          const x0 = margin.left + index * groupWidth + 2;
          series.forEach(({ key, color }, seriesIndex) => {
            const value = Number(row[key] || 0);
            const y = yScale(value);
            const h = margin.top + innerHeight - y;
            body += '<rect x="' + (x0 + seriesIndex * barWidth) + '" y="' + y + '" width="' + (barWidth - 1) + '" height="' + h + '" rx="2" fill="' + color + '"><title>' + html(row.ts + ": " + fmt(value) + unit) + '</title></rect>';
          });
        });

        [0, Math.floor(points.length / 2), points.length - 1].forEach((index) => {
          const x = margin.left + index * groupWidth + groupWidth / 2;
          body += '<text class="axis" x="' + x + '" y="' + (height - 15) + '" text-anchor="middle">' + secondsLabel(points[index].__ms, minMs) + '</text>';
        });

        document.getElementById(targetId).innerHTML = chartFrame(width, height, body);
      }

      function renderLogs() {
        const rows = report.results.logs.data.slice(0, 16);
        document.getElementById("logs").innerHTML = rows.map((row) =>
          '<div class="log-row"><div class="log-time">' + html(row.ts) + '</div><div class="log-line">' + html(row.log_line) + '</div></div>'
        ).join("");
      }

      renderSummary();
      renderHorizontalBars("event-counts", report.results.eventCounts.data);
      renderStackedTimeline("event-timeline", "timeline-legend", report.results.timeline.data);
      renderLineChart("cpu-chart", report.results.hypervisor.data, [
        { key: "process_cpu_percent", color: colors.orange },
        { key: "sandbox_cgroup_cpu_percent", color: colors.purple },
      ], "%");
      renderLineChart("memory-chart", report.results.hypervisor.data, [
        { key: "process_rss_mib", color: colors.blue },
        { key: "sandbox_cgroup_memory_mib", color: colors.green },
      ], " MiB");
      renderGroupedBars("io-chart", report.results.io.data, [
        { key: "rootfs_read_mib", color: colors.cyan },
        { key: "rootfs_write_mib", color: colors.pink },
      ], " MiB");
      renderLineChart("vmm-chart", report.results.io.data, [
        { key: "vcpu_exits", color: colors.red },
        { key: "uart_writes", color: colors.slate },
      ], "");
      renderLogs();
      document.getElementById("raw-json").textContent = JSON.stringify(report.results, null, 2);
    </script>
  </body>
</html>
`;
}

function escapeHtml(value) {
  return String(value)
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;");
}
