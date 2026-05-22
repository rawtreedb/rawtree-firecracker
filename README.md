# RawTree Firecracker Observability

Reference implementation for adding RawTree observability to a sandbox platform that creates Firecracker microVMs internally.

This repository is intentionally provider-side. It does not create a competing sandbox SDK, it does not ask sandbox users to wrap their sandbox objects, and it does not inject a RawTree guest agent. It only uses the Firecracker control API plus provider lifecycle events.

The model is:

```txt
your public sandbox API
  -> your internal sandbox control plane
  -> Firecracker microVM creation
  -> Firecracker-native logs and metrics
  -> RawTree event stream
```

Users keep using the provider's normal sandbox API. The platform team enables observability once in the internal Firecracker launch path.

## Why This Exists

AI agents increasingly run inside short-lived sandboxes. Sandbox providers usually need to answer questions like:

- Was the microVM created and started successfully?
- Which Firecracker configuration was used?
- Did Firecracker emit warnings or errors?
- What block, network, vsock, vCPU, API, and latency counters did Firecracker report?
- Did the sandbox stop cleanly or did the provider terminate it?
- Which team, project, region, runtime, or image did this sandbox belong to?

For a platform that already owns Firecracker VM creation, the cleanest place to add this visibility is the Firecracker API path.

## Architecture

```txt
Sandbox provider control plane
  |
  | 1. receives internal create-sandbox request
  v
Firecracker orchestrator
  |
  | 2. starts RawTree host collector with provider metadata
  | 3. creates host files for Firecracker logger and metrics
  | 4. configures Firecracker /logger and /metrics
  | 5. configures kernel, rootfs, machine, and optional network
  | 6. starts the microVM
  | 7. samples Firecracker host PID and cgroup
  v
Firecracker VMM
  |
  | 8. writes logs and metrics on the host
  v
RawTree host collector
  |
  | 9. emits provider lifecycle, hypervisor sample, VMM log, and VMM metric events
  v
RawTree API
```

The RawTree API key stays on the host side. The guest VM receives no RawTree credentials and no RawTree process is installed inside it.

## What Changes For Sandbox Users

Nothing.

They keep doing whatever the provider already exposes:

```ts
const sandbox = await Sandbox.create();
await sandbox.runCommand({ cmd: "npm", args: ["test"] });
```

The provider's internal platform code changes:

```txt
before Firecracker InstanceStart:
  start RawTree host collector
  configure Firecracker /logger
  configure Firecracker /metrics
  configure the normal microVM

while the VM runs:
  keep provider lifecycle correlated by sandbox_id and run_id
  sample host process/cgroup CPU and memory for the Firecracker process

when the provider stops the VM:
  call FlushMetrics
  terminate or wait for Firecracker
  read Firecracker log and metrics outputs
  flush RawTree events
```

## What This Repository Contains

```txt
src/
  orchestrator.ts                  # internal provider launch flow
  firecracker-api.ts               # direct Firecracker Unix-socket API calls
  firecracker-native-collector.ts  # Firecracker log/metrics files -> RawTree events
  hypervisor-sampler.ts            # host process/cgroup metrics -> RawTree events
  host-collector.ts                # RawTree writer with provider metadata enrichment
  types.ts
```

This is a reference design for provider integration. A production provider would usually wire these pieces into its existing control plane rather than run this CLI directly.

## Firecracker API Calls

This project does not use a Firecracker SDK. Firecracker exposes an HTTP API over a Unix socket.

The orchestrator starts Firecracker:

```ts
spawn("/usr/local/bin/firecracker", [
  "--api-sock",
  "/tmp/firecracker.socket",
]);
```

Then it calls the real Firecracker API:

```txt
PUT /logger
PUT /metrics
PUT /machine-config
PUT /boot-source
PUT /drives/rootfs
PUT /network-interfaces/eth0   # optional
PUT /actions { "action_type": "InstanceStart" }
PUT /actions { "action_type": "FlushMetrics" }
```

Logger configuration:

```json
{
  "log_path": "/tmp/rawtree-sandbox/firecracker.log",
  "level": "Info",
  "show_level": true,
  "show_log_origin": true
}
```

Metrics configuration:

```json
{
  "metrics_path": "/tmp/rawtree-sandbox/firecracker.metrics.jsonl"
}
```

Firecracker writes VMM logs and metrics to host files. The RawTree host collector reads those files, enriches each event with provider metadata, and inserts the events into RawTree.

## Boot Lifecycle

Startup:

1. Provider receives an internal sandbox creation request.
2. Provider allocates `sandbox_id` and `run_id`.
3. Provider starts the RawTree host collector.
4. Provider creates host files for Firecracker logs and metrics.
5. Provider starts the Firecracker process with an API socket.
6. Provider calls `/logger` and `/metrics`.
7. Provider configures the machine, boot source, rootfs, and optional network.
8. Provider starts the microVM with `InstanceStart`.

Shutdown:

1. Provider decides the sandbox should stop, or the Firecracker process exits.
2. Provider calls `FlushMetrics` while Firecracker is still alive.
3. Provider waits for or terminates Firecracker.
4. Host collector reads Firecracker logs and metrics from the host files.
5. Host collector emits the VM stopped event.
6. Host collector flushes pending RawTree writes and closes.

## What We Collect

Provider lifecycle events:

- sandbox create started/failed
- VM started/stopped
- Firecracker exit code
- stop reason
- provider metadata
- sandbox id and run id

Firecracker logger events:

- VMM log line
- sandbox id and run id
- provider metadata

Firecracker metrics events:

- raw Firecracker metrics JSON
- block, net, vsock, vCPU, API, latency, logger, seccomp, and VMM counters when emitted by Firecracker
- sandbox id and run id
- provider metadata

Hypervisor sample events:

- raw host-side Firecracker process and cgroup JSON
- process RSS, virtual size, CPU ticks, file descriptor count, context switches, and `/proc/<pid>/io`
- cgroup v2 CPU usage and memory usage when available
- sandbox id and run id
- provider metadata

In this standalone demo, cgroup values reflect whatever cgroup the Firecracker process runs inside. For production-quality per-sandbox CPU and memory, place each microVM in its own provider-owned cgroup and sample that cgroup. The process RSS fields are still specific to the Firecracker process.

## What We Do Not Collect

Because this version only uses Firecracker APIs, it does not automatically observe arbitrary activity inside the guest OS:

- commands run inside the sandbox
- subprocess trees
- file mutations inside the rootfs
- stdout/stderr from the workload
- exact HTTP URLs or TLS payloads
- guest process memory by command

It does sample the host Firecracker process and cgroup. That gives provider-style hypervisor/process telemetry, not per-process memory inside the guest OS.

Those should come from the provider's existing sandbox control plane if it already has exec/files/logs APIs. RawTree can ingest those provider-native events too, but this repo intentionally keeps the Firecracker example limited to Firecracker-native APIs.

## Requirements

Host:

- Linux host with KVM and access to `/dev/kvm`
- Firecracker binary
- kernel image compatible with Firecracker
- rootfs image compatible with the kernel
- outbound network access from the host collector to RawTree
- Node.js 22+

For AWS testing, use an EC2 `.metal` instance type, such as `c5.metal`. Standard
virtualized EC2 instances can boot Linux, but they usually do not expose
`/dev/kvm`, so Firecracker cannot start a real microVM there. The setup check
should include:

```bash
ls -l /dev/kvm
[ -r /dev/kvm ] && [ -w /dev/kvm ] && echo "KVM ready"
```

Guest rootfs:

- whatever your provider normally boots
- no RawTree process required
- no Node.js or `socat` required

For local development on macOS, use `--dry-run`. A real Firecracker boot requires Linux + KVM.

## Install

```bash
npm install
```

Copy the env example if you want to run against RawTree:

```bash
cp .env.example .env
```

Then export your key in the shell before a real run:

```bash
export RAWTREE_API_KEY=rt_...
```

## Dry Run

Dry run does not require Linux, KVM, Firecracker, or a real rootfs. It prints the platform integration plan.

```bash
npm run dry-run
```

Or pass explicit values:

```bash
npm run start -- --dry-run \
  --kernel /var/lib/firecracker/vmlinux \
  --rootfs /var/lib/firecracker/rootfs.ext4 \
  --metadata provider=example \
  --metadata environment=poc
```

## Real Run

Run this on a Linux host with KVM:

```bash
sudo -E npm run start -- \
  --firecracker /usr/local/bin/firecracker \
  --kernel /var/lib/firecracker/vmlinux \
  --rootfs /var/lib/firecracker/rootfs.ext4 \
  --metadata provider=example \
  --metadata environment=poc
```

The CLI also accepts `--api-key`, but passing the key through `RAWTREE_API_KEY` avoids printing it in package-manager command echoes.

Optional TAP networking:

```bash
sudo -E npm run start -- \
  --firecracker /usr/local/bin/firecracker \
  --kernel /var/lib/firecracker/vmlinux \
  --rootfs /var/lib/firecracker/rootfs.ext4 \
  --tap tap0 \
  --guest-mac AA:FC:00:00:00:01
```

The demo has a default `--run-timeout-ms 30000`. When that timeout is reached, it calls `FlushMetrics`, terminates Firecracker, reads the Firecracker log and metrics files, and sends the resulting events to RawTree. In a real provider platform, the stop signal would come from the provider's normal sandbox lifecycle.

## Rich Example

The rich example prepares a copy of the rootfs with a small boot-time workload.
That workload writes and reads temporary files and burns CPU for a few short
bursts. The provider process also moves Firecracker into a dedicated cgroup and
flushes Firecracker metrics every two seconds, so RawTree receives a useful time
series instead of only a final metric snapshot.

Run it on a Linux host with KVM:

```bash
export RAWTREE_API_KEY=rt_...
npm run rich-example
```

What this produces:

- provider lifecycle events
- host hypervisor samples every second
- periodic Firecracker VMM metrics every two seconds
- Firecracker VMM log lines
- metadata marking the run as `scenario=rich-example`

The example prints the `run_id`. Use that id with the SQL files in `sql/`:

```bash
RUN_ID=rt_firecracker_sandbox_run_...
SQL=$(sed "s/<RUN_ID>/$RUN_ID/g" sql/05_run_summary.sql)
rtree query --sql "$SQL"
```

Generate a standalone HTML report from those SQL-backed views:

```bash
RAWTREE_API_KEY=rt_... npm run rich-report -- "$RUN_ID"
```

Useful files:

- `sql/00_event_counts.sql`: event volume by event type
- `sql/01_event_timeline.sql`: event timeline by second
- `sql/02_hypervisor_cpu_memory.sql`: host process and cgroup CPU/memory
- `sql/03_firecracker_io_metrics.sql`: rootfs IO and vCPU exit counters
- `sql/04_firecracker_logs.sql`: Firecracker VMM log lines
- `sql/05_run_summary.sql`: one-row run summary

## Example Events

Provider lifecycle:

```json
{
  "event_type": "sandbox.firecracker.provider.vm.started",
  "event_time": "2026-05-21T12:00:00.000Z",
  "sampled_at": "2026-05-21T12:00:00.000Z",
  "provider": "firecracker-sandbox-provider",
  "sandbox_id": "sbx_123",
  "run_id": "rt_firecracker_sandbox_run_456",
  "source": "firecracker_host_collector",
  "boot_args": "console=ttyS0 root=/dev/vda rw reboot=k panic=1 pci=off",
  "metadata": {
    "provider": "example",
    "environment": "poc"
  }
}
```

Firecracker log:

```json
{
  "event_type": "sandbox.firecracker.vmm.log",
  "sampled_at": "2026-05-21T12:00:00.000Z",
  "source": "firecracker_vmm_logger",
  "firecracker": {
    "log": {
      "line": "2026-05-21T12:00:00 [anonymous-instance:main] Running Firecracker..."
    }
  },
  "sandbox_id": "sbx_123",
  "run_id": "rt_firecracker_sandbox_run_456"
}
```

Firecracker metrics:

```json
{
  "event_type": "sandbox.firecracker.vmm.metrics",
  "sampled_at": "2026-05-21T12:00:00.000Z",
  "source": "firecracker_vmm_metrics",
  "firecracker": {
    "metrics": {
      "block_rootfs": {
        "read_bytes": 41401344
      }
    }
  },
  "sandbox_id": "sbx_123",
  "run_id": "rt_firecracker_sandbox_run_456"
}
```

Query useful counters from the nested metrics object:

```sql
SELECT
  event_time,
  toUInt64OrZero(toString(`firecracker.metrics.block_rootfs.read_bytes`)) AS rootfs_read_bytes,
  toUInt64OrZero(toString(`firecracker.metrics.block_rootfs.write_bytes`)) AS rootfs_write_bytes,
  toUInt64OrZero(toString(`firecracker.metrics.block_rootfs.read_count`)) AS rootfs_read_count,
  toUInt64OrZero(toString(`firecracker.metrics.block_rootfs.write_count`)) AS rootfs_write_count,
  toUInt64OrZero(toString(`firecracker.metrics.vcpu.exit_io_in`))
    + toUInt64OrZero(toString(`firecracker.metrics.vcpu.exit_io_out`))
    + toUInt64OrZero(toString(`firecracker.metrics.vcpu.exit_mmio_read`))
    + toUInt64OrZero(toString(`firecracker.metrics.vcpu.exit_mmio_write`)) AS vcpu_exits,
  toUInt64OrZero(toString(`firecracker.metrics.uart.write_count`)) AS uart_writes,
  toUInt64OrZero(toString(`firecracker.metrics.interrupts.triggers`)) AS interrupts
FROM sandbox_events
WHERE toString(event_type) = 'sandbox.firecracker.vmm.metrics'
ORDER BY event_time DESC
LIMIT 100;
```

Hypervisor sample:

```json
{
  "event_type": "sandbox.hypervisor.sample",
  "sampled_at": "2026-05-21T12:00:00.000Z",
  "source": "host_hypervisor_sampler",
  "hypervisor": {
    "pid": 1234,
    "process": {
      "fd_count": 33,
      "status": {
        "vm_rss_bytes": 14295040
      },
      "stat": {
        "cpu_total_ticks": 149
      }
    },
    "cgroup": {
      "memory_current_bytes": 268435456,
      "cpu_stat": {
        "usage_usec": 4466298
      }
    }
  },
  "sandbox_id": "sbx_123",
  "run_id": "rt_firecracker_sandbox_run_456"
}
```

Query host-side CPU and memory from the nested hypervisor object:

```sql
SELECT
  event_time,
  toUInt64OrZero(toString(`hypervisor.process.status.vm_rss_bytes`)) AS firecracker_rss_bytes,
  toUInt64OrZero(toString(`hypervisor.process.status.vm_size_bytes`)) AS firecracker_vm_size_bytes,
  toUInt64OrZero(toString(`hypervisor.process.stat.cpu_total_ticks`)) AS firecracker_cpu_ticks,
  toUInt64OrZero(toString(`hypervisor.process.fd_count`)) AS firecracker_fd_count,
  toUInt64OrZero(toString(`hypervisor.cgroup.memory_current_bytes`)) AS cgroup_memory_current_bytes,
  toUInt64OrZero(toString(`hypervisor.cgroup.cpu_stat.usage_usec`)) AS cgroup_cpu_usage_usec
FROM sandbox_events
WHERE toString(event_type) = 'sandbox.hypervisor.sample'
ORDER BY event_time DESC
LIMIT 100;
```

## Integration Checklist For A Sandbox Provider

1. Decide where in the internal sandbox creation path to create a RawTree run id.
2. Start a host-side RawTree collector beside the Firecracker process.
3. Configure Firecracker `/logger` with a per-sandbox host log path.
4. Configure Firecracker `/metrics` with a per-sandbox host metrics path.
5. Sample host-side hypervisor/process metrics for the Firecracker process or its cgroup.
6. Attach provider metadata such as team, project, region, runtime, image id, and sandbox id.
7. Call `FlushMetrics` before stopping the VM when possible.
8. Emit Firecracker logs, Firecracker metrics, and hypervisor samples to RawTree.
9. Optionally also emit provider-native exec/files/stdout/stderr events if your platform already has them.

## Production Notes

This repo optimizes for showing the Firecracker-native integration shape clearly.

For production, likely next steps are:

- streaming log and metrics readers instead of final file reads
- bounded memory queue in host collector
- batched RawTree inserts
- retry/backoff and disk spill for collector failures
- provider-owned cgroup isolation per sandbox
- provider-specific redaction
- explicit event schema versioning
- optional ingestion of provider-native sandbox events
