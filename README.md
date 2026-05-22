# RawTree Firecracker Observability

Reference implementation for adding RawTree observability to a sandbox platform that creates Firecracker microVMs internally.

This repository is intentionally provider-side. It does not create a competing sandbox SDK and it does not ask sandbox users to wrap their sandbox objects. It uses the official `firecracker-go-sdk` to drive the Firecracker control API, then pushes Firecracker-native logs, Firecracker-native metrics, provider lifecycle events, sandbox exec events, and host-side hypervisor samples to RawTree.

The RawTree API key and RawTree writer stay on the host. For the Vercel-like `create` / `exec` / `stop` lifecycle demo, the provider injects a small guest control process and talks to it over Firecracker vsock. That guest process is not a RawTree agent: it has no RawTree credentials, and it only exists so the provider control plane can run commands in the microVM after boot.

The model is:

```txt
your public sandbox API
  -> your internal sandbox control plane
  -> Firecracker microVM creation
  -> optional provider control agent over vsock
  -> Firecracker-native logs and metrics
  -> provider exec/lifecycle events
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
  | 4. injects provider control agent into the rootfs copy
  | 5. builds firecracker-go-sdk Config with a vsock device
  | 6. SDK configures logger, metrics, kernel, rootfs, machine, vsock, and optional network
  | 7. SDK starts the microVM
  | 8. samples Firecracker host PID and cgroup
  v
Firecracker VMM
  |
  | 9. writes logs and metrics on the host
  | 10. exposes provider exec channel over vsock
  v
RawTree host collector
  |
  | 11. emits provider lifecycle, exec, hypervisor sample, VMM log, and VMM metric events
  v
RawTree API
```

The RawTree API key stays on the host side. The guest VM receives no RawTree credentials.

## What Changes For Sandbox Users

Nothing.

They keep doing whatever the provider already exposes:

```ts
const sandbox = await Sandbox.create();
await sandbox.runCommand({ cmd: "npm", args: ["test"] });
```

This reference CLI exposes the same shape:

```bash
# Create a basic sandbox.
go run . create

# Create a sandbox with 1 vCPU and open an interactive shell.
go run . create --vcpus 1 --connect

# Create a Python sandbox with a custom timeout.
go run . create --runtime python3.13 --timeout 1h

# Execute a command after the sandbox is already running.
go run . exec sb_1234567890 ls -la

# Execute with environment variables and a working directory.
go run . exec --env DEBUG=true --workdir /app sb_1234567890 npm test

# Stop one or more sandboxes.
go run . stop sb_1234567890 sb_0987654321
```

In this reference, `--runtime` is provider metadata. In a real platform it would usually select the rootfs, image, snapshot, or language layer before the Firecracker VM is created.

The provider's internal platform code changes:

```txt
before Firecracker InstanceStart:
  start RawTree host collector
  copy/inject the provider control agent into the rootfs or image layer
  configure Firecracker logger and metrics paths through firecracker-go-sdk
  configure a Firecracker vsock device for provider exec
  configure the normal microVM

while the VM runs:
  keep provider lifecycle correlated by sandbox_id and run_id
  sample host process/cgroup CPU and memory for the Firecracker process
  send exec requests over vsock and emit exec started/output/completed events

when the provider stops the VM:
  call FlushMetrics
  terminate or wait for Firecracker
  read Firecracker log and metrics outputs
  flush RawTree events
```

## What This Repository Contains

```txt
rawtree_firecracker_observability.go
                                  # CLI wrapper around create/exec/stop and legacy launch flow
agent_linux.go                    # guest provider-control process for vsock exec

internal/
  orchestrator.go                 # Linux/KVM Firecracker launch via firecracker-go-sdk
  control_linux.go                # host-side exec/stop control over Firecracker vsock
  state.go                        # sandbox state files for create/exec/stop
  firecracker_native.go           # Firecracker log/metrics files -> RawTree events
  hypervisor_sampler.go           # host process/cgroup metrics -> RawTree events
  collector.go                    # RawTree writer with provider metadata enrichment
  types.go

scripts/
  prepare-rich-rootfs.sh          # optional legacy helper for boot-time workload experiments
  generate-rich-report.mjs        # queries RawTree SQL views and writes an HTML report

sql/
  *.sql                           # SQL views for counts, timelines, CPU, memory, IO, logs
```

This is a reference design for provider integration. A production provider would usually wire these pieces into its existing control plane rather than run this CLI directly.

## Firecracker API Calls

This project uses `github.com/firecracker-microvm/firecracker-go-sdk`. The SDK still talks to the real Firecracker HTTP API over the Unix socket, but it removes the need for us to hand-roll request ordering, model structs, socket clients, process startup, and sync actions.

The SDK-backed orchestrator builds a `firecracker.Config`:

```go
cfg := firecracker.Config{
  SocketPath:      paths.APISocketPath,
  LogPath:         paths.FirecrackerLogPath,
  LogLevel:        "Info",
  MetricsPath:     paths.FirecrackerMetricsPath,
  KernelImagePath: request.Firecracker.Kernel,
  KernelArgs:      bootArgs(request.Firecracker),
  Drives: firecracker.NewDrivesBuilder(paths.RootFSCopyPath).
    WithRootDrive(paths.RootFSCopyPath, firecracker.WithDriveID("rootfs")).
    Build(),
  VsockDevices: []firecracker.VsockDevice{
    {
      ID:   "control",
      Path: paths.VsockPath,
      CID:  request.Vsock.GuestCID,
    },
  },
  MachineCfg: models.MachineConfiguration{
    MemSizeMib: firecracker.Int64(request.Firecracker.MemMiB),
    VcpuCount:  firecracker.Int64(request.Firecracker.VCPUCount),
    Smt:        firecracker.Bool(false),
  },
}
```

Then it creates and starts a machine:

```go
machine, err := firecracker.NewMachine(
  ctx,
  cfg,
  firecracker.WithProcessRunner(cmd),
  firecracker.WithLogger(logger),
  moveCgroup,
)
err = machine.Start(ctx)
```

Under the hood, the SDK configures the same Firecracker API surfaces:

```txt
PUT /logger
PUT /metrics
PUT /machine-config
PUT /boot-source
PUT /drives/rootfs
PUT /vsock/control
PUT /network-interfaces/eth0   # optional
PUT /actions { "action_type": "InstanceStart" }
PUT /actions { "action_type": "FlushMetrics" }
```

The final metrics flush also goes through the SDK client:

```go
client := firecracker.NewClient(socketPath, logger, false)
action := models.InstanceActionInfoActionTypeFlushMetrics
_, err := client.CreateSyncAction(ctx, &models.InstanceActionInfo{
  ActionType: &action,
})
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
5. Provider copies the rootfs and injects the provider control process into that copy.
6. Provider builds `firecracker.Config` with log, metrics, machine, boot, drive, vsock, and optional network settings.
7. Provider creates a `firecracker.Machine`.
8. The SDK starts Firecracker with the API socket and applies the config.
9. The SDK starts the microVM with `InstanceStart`.
10. The guest starts the provider control process, which listens on the configured vsock port.

Exec:

1. Provider receives an exec request for a running sandbox.
2. Provider opens the host side of the Firecracker vsock device.
3. Provider sends command, environment, working directory, and interactivity options as JSON.
4. Guest control process starts the command and streams stdout/stderr frames back over vsock.
5. Host control plane emits `sandbox.exec.*` events to RawTree.

Shutdown:

1. Provider decides the sandbox should stop, or the Firecracker process exits.
2. Provider calls `FlushMetrics` while Firecracker is still alive.
3. Provider writes a stop marker and terminates Firecracker.
4. Supervisor waits for Firecracker to exit.
5. Host collector reads Firecracker logs and metrics from the host files.
6. Host collector emits the VM stopped event.
7. Host collector flushes pending RawTree writes and closes.

## What We Collect

Provider lifecycle events:

- sandbox create started/failed
- VM started/stopped
- Firecracker exit code
- stop reason
- provider metadata
- sandbox id and run id

Sandbox exec events:

- command requested through the provider exec API
- environment values passed by the provider
- working directory, sudo, and interactive flags
- guest PID when the process starts
- stdout/stderr chunk size and preview
- exit code and duration

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

Because this version observes Firecracker plus the provider exec control path, it still does not automatically observe arbitrary activity inside the guest OS:

- commands not launched through the provider exec API
- subprocess trees after a command starts
- file mutations inside the rootfs unless the provider emits them
- exact HTTP URLs or TLS payloads
- guest process memory by command

It does sample the host Firecracker process and cgroup. That gives provider-style hypervisor/process telemetry, not per-process memory inside the guest OS. The injected guest process exists only to implement provider exec; RawTree ingestion remains host-side.

Those should come from the provider's existing sandbox control plane if it already has files, process tree, network, or workload-log APIs. RawTree can ingest those provider-native events too.

## Requirements

Host:

- Linux host with KVM and access to `/dev/kvm`
- Go 1.22+
- Node.js 22+ for HTML report generation
- Firecracker binary
- kernel image compatible with Firecracker
- rootfs image compatible with the kernel
- outbound network access from the host collector to RawTree

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
- systemd for this reference injection path, or an equivalent provider-owned init hook
- no RawTree process or RawTree credentials required
- no Node.js or `socat` required

For local development on macOS, use `--dry-run`. A real Firecracker boot requires Linux + KVM.

## Install

```bash
go mod download
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
go run . \
  --dry-run \
  --kernel /var/lib/firecracker/vmlinux \
  --rootfs /var/lib/firecracker/rootfs.ext4
```

Or pass explicit values:

```bash
go run . \
  --dry-run \
  --kernel /var/lib/firecracker/vmlinux \
  --rootfs /var/lib/firecracker/rootfs.ext4 \
  --metadata provider=example \
  --metadata environment=poc
```

## Sandbox Lifecycle Run

Run this on a Linux host with KVM. The CLI also accepts `--api-key`, but passing the key through `RAWTREE_API_KEY` avoids printing it in shell command arguments.

Create a sandbox:

```bash
export RAWTREE_API_KEY=rt_...

sudo -E go run . create \
  --firecracker /usr/local/bin/firecracker \
  --kernel /var/lib/firecracker/vmlinux \
  --rootfs /var/lib/firecracker/rootfs.ext4 \
  --runtime node \
  --timeout 1h \
  --metadata provider=example \
  --metadata environment=poc
```

Optional TAP networking for sandbox commands that need outbound internet:

```bash
sudo -E go run . create \
  --firecracker /usr/local/bin/firecracker \
  --kernel /var/lib/firecracker/vmlinux \
  --rootfs /var/lib/firecracker/rootfs.ext4 \
  --runtime node \
  --tap rtap0 \
  --guest-mac AA:FC:00:00:00:02
```

The command prints:

```txt
sandbox_id=sb_...
run_id=rt_firecracker_sandbox_run_...
state_file=/tmp/rawtree-firecracker/sandboxes/sb_....json
```

Run commands in that already-created sandbox:

```bash
sudo -E go run . exec sb_... ls -la
sudo -E go run . exec --env DEBUG=true sb_... sh -lc 'echo "$DEBUG"'
sudo -E go run . exec --workdir /app sb_... npm test
sudo -E go run . exec --interactive --sudo sb_... sh
```

Stop the sandbox:

```bash
sudo -E go run . stop sb_...
```

Under the hood, `create` starts a supervisor process that owns the Firecracker VM. `exec` and `stop` are separate CLI calls that read the sandbox state file and talk to the running VM/control plane. In production, those are the provider's internal API handlers rather than command-line calls.

### Node Sandbox Demo

This flow mirrors a common agent workload: start a Node sandbox, clone an npm package, install dependencies, run tests, stop the sandbox, and generate the RawTree report. The rootfs must already contain `node`, `npm`, `git`, and `curl`; `--runtime node` is metadata used by the provider layer.

```bash
cd /home/ubuntu/rawtree-firecracker-observability
export RAWTREE_API_KEY=rt_...
export RAWTREE_SANDBOX_TABLE=sandbox_events

export TAP=rtap0
export SUBNET=172.16.0.0/24
export HOST_IP=172.16.0.1
export EXT_IF="$(ip route show default | sed -n 's/.* dev \([^ ]*\).*/\1/p' | head -n 1)"

sudo ip link del "$TAP" 2>/dev/null || true
sudo ip tuntap add dev "$TAP" mode tap
sudo ip addr add "$HOST_IP/24" dev "$TAP"
sudo ip link set "$TAP" up
sudo sysctl -w net.ipv4.ip_forward=1
sudo iptables -t nat -C POSTROUTING -s "$SUBNET" -o "$EXT_IF" -j MASQUERADE 2>/dev/null || sudo iptables -t nat -A POSTROUTING -s "$SUBNET" -o "$EXT_IF" -j MASQUERADE
sudo iptables -C FORWARD -i "$TAP" -o "$EXT_IF" -j ACCEPT 2>/dev/null || sudo iptables -A FORWARD -i "$TAP" -o "$EXT_IF" -j ACCEPT
sudo iptables -C FORWARD -i "$EXT_IF" -o "$TAP" -m state --state RELATED,ESTABLISHED -j ACCEPT 2>/dev/null || sudo iptables -A FORWARD -i "$EXT_IF" -o "$TAP" -m state --state RELATED,ESTABLISHED -j ACCEPT

CREATE_OUTPUT="$(sudo --preserve-env=RAWTREE_API_KEY,RAWTREE_SANDBOX_TABLE go run . create \
  --runtime node \
  --rootfs /var/lib/firecracker/rootfs-node.ext4 \
  --kernel /var/lib/firecracker/vmlinux \
  --firecracker /usr/local/bin/firecracker \
  --vcpus 1 \
  --mem-mib 512 \
  --timeout 30m \
  --tap "$TAP" \
  --guest-mac AA:FC:00:00:00:02 \
  --metadata demo=node-npm-tests \
  --metadata repo=is-odd)"

echo "$CREATE_OUTPUT"
SANDBOX_ID="$(printf "%s\n" "$CREATE_OUTPUT" | sed -n "s/^sandbox_id=//p")"
RUN_ID="$(printf "%s\n" "$CREATE_OUTPUT" | sed -n "s/^run_id=//p")"
echo "SANDBOX_ID=$SANDBOX_ID"
echo "RUN_ID=$RUN_ID"

sudo --preserve-env=RAWTREE_API_KEY,RAWTREE_SANDBOX_TABLE go run . exec "$SANDBOX_ID" sh -lc '
set -eu
ip addr add 172.16.0.2/24 dev eth0 || true
ip link set eth0 up
ip route replace default via 172.16.0.1
printf "nameserver 1.1.1.1\n" > /etc/resolv.conf
curl -fsSL --connect-timeout 5 --max-time 20 https://api.github.com/zen
'

sudo --preserve-env=RAWTREE_API_KEY,RAWTREE_SANDBOX_TABLE go run . exec "$SANDBOX_ID" sh -lc '
node --version
npm --version
git --version
'

sudo --preserve-env=RAWTREE_API_KEY,RAWTREE_SANDBOX_TABLE go run . exec "$SANDBOX_ID" sh -lc '
set -eu
rm -rf /workspace
mkdir -p /workspace
git clone --depth 1 https://github.com/jonschlinkert/is-odd.git /workspace/is-odd
cd /workspace/is-odd
git log --oneline -1
'

sudo --preserve-env=RAWTREE_API_KEY,RAWTREE_SANDBOX_TABLE go run . exec "$SANDBOX_ID" sh -lc '
set -eu
cd /workspace/is-odd
npm install
'

sudo --preserve-env=RAWTREE_API_KEY,RAWTREE_SANDBOX_TABLE go run . exec "$SANDBOX_ID" sh -lc '
set -eu
cd /workspace/is-odd
npm test
'

sudo --preserve-env=RAWTREE_API_KEY,RAWTREE_SANDBOX_TABLE go run . stop "$SANDBOX_ID"
RAWTREE_API_KEY="$RAWTREE_API_KEY" node scripts/generate-rich-report.mjs "$RUN_ID"
```

The legacy one-shot demo is still available by passing the root flags directly:

```bash
sudo -E go run . \
  --firecracker /usr/local/bin/firecracker \
  --kernel /var/lib/firecracker/vmlinux \
  --rootfs /var/lib/firecracker/rootfs.ext4
```

## Rich Example

The rich example creates a sandbox, then runs multiple commands through the exec API. The workload writes and reads temporary files and burns CPU for a few short bursts. The provider process also moves Firecracker into a dedicated cgroup and flushes Firecracker metrics every two seconds, so RawTree receives a useful time series instead of only a final metric snapshot.

Run it on a Linux host with KVM:

```bash
export RAWTREE_API_KEY=rt_...
bash examples/rich-firecracker-workload.sh
```

What this produces:

- provider lifecycle events
- sandbox exec command/output/completion events
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
RAWTREE_API_KEY=rt_... node scripts/generate-rich-report.mjs "$RUN_ID"
```

Run the local checks:

```bash
go test ./...
GOOS=linux GOARCH=amd64 go test ./...
node --check scripts/generate-rich-report.mjs
```

Useful files:

- `sql/00_event_counts.sql`: event volume by event type
- `sql/01_event_timeline.sql`: event timeline by second
- `sql/02_hypervisor_cpu_memory.sql`: host process and cgroup CPU/memory
- `sql/03_firecracker_io_metrics.sql`: rootfs IO and vCPU exit counters
- `sql/04_firecracker_logs.sql`: Firecracker VMM log lines
- `sql/05_run_summary.sql`: one-row run summary
- `sql/06_exec_activity.sql`: command and output events from the vsock exec API

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

Sandbox exec output:

```json
{
  "event_type": "sandbox.exec.output",
  "event_time": "2026-05-21T12:00:01.000Z",
  "sampled_at": "2026-05-21T12:00:01.000Z",
  "provider": "firecracker-sandbox-provider",
  "sandbox_id": "sb_123",
  "run_id": "rt_firecracker_sandbox_run_456",
  "source": "sandbox_vsock_control",
  "status": "success",
  "exec_id": "6f0f2c72-0732-4f55-9cf5-90154d5cfba7",
  "stream": "stdout",
  "chunk_bytes": 18,
  "chunk_preview": "hello from sandbox\n"
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
      },
      "block": {
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
WITH
  toUInt64OrZero(toString(`firecracker.metrics.block_rootfs.read_bytes`)) AS rootfs_read_bytes_named,
  toUInt64OrZero(toString(`firecracker.metrics.block_root_drive.read_bytes`)) AS rootfs_read_bytes_sdk_default,
  toUInt64OrZero(toString(`firecracker.metrics.block.read_bytes`)) AS rootfs_read_bytes_aggregate,
  toUInt64OrZero(toString(`firecracker.metrics.block_rootfs.write_bytes`)) AS rootfs_write_bytes_named,
  toUInt64OrZero(toString(`firecracker.metrics.block_root_drive.write_bytes`)) AS rootfs_write_bytes_sdk_default,
  toUInt64OrZero(toString(`firecracker.metrics.block.write_bytes`)) AS rootfs_write_bytes_aggregate,
  toUInt64OrZero(toString(`firecracker.metrics.block_rootfs.read_count`)) AS rootfs_read_count_named,
  toUInt64OrZero(toString(`firecracker.metrics.block_root_drive.read_count`)) AS rootfs_read_count_sdk_default,
  toUInt64OrZero(toString(`firecracker.metrics.block.read_count`)) AS rootfs_read_count_aggregate,
  toUInt64OrZero(toString(`firecracker.metrics.block_rootfs.write_count`)) AS rootfs_write_count_named,
  toUInt64OrZero(toString(`firecracker.metrics.block_root_drive.write_count`)) AS rootfs_write_count_sdk_default,
  toUInt64OrZero(toString(`firecracker.metrics.block.write_count`)) AS rootfs_write_count_aggregate
SELECT
  event_time,
  if(rootfs_read_bytes_named > 0, rootfs_read_bytes_named, if(rootfs_read_bytes_sdk_default > 0, rootfs_read_bytes_sdk_default, rootfs_read_bytes_aggregate)) AS rootfs_read_bytes,
  if(rootfs_write_bytes_named > 0, rootfs_write_bytes_named, if(rootfs_write_bytes_sdk_default > 0, rootfs_write_bytes_sdk_default, rootfs_write_bytes_aggregate)) AS rootfs_write_bytes,
  if(rootfs_read_count_named > 0, rootfs_read_count_named, if(rootfs_read_count_sdk_default > 0, rootfs_read_count_sdk_default, rootfs_read_count_aggregate)) AS rootfs_read_count,
  if(rootfs_write_count_named > 0, rootfs_write_count_named, if(rootfs_write_count_sdk_default > 0, rootfs_write_count_sdk_default, rootfs_write_count_aggregate)) AS rootfs_write_count,
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
