# Provider Integration Notes

This document maps the reference implementation to the internal systems a sandbox provider usually already has.

## Where To Integrate

The integration belongs in the internal create-sandbox path, before Firecracker receives `InstanceStart`.

```txt
create sandbox request
  -> allocate sandbox id and run id
  -> select rootfs/image
  -> start RawTree host collector
  -> create Firecracker log and metrics files
  -> build firecracker-go-sdk Config
  -> SDK configures Firecracker logger, metrics, boot source, drives, machine, and optional network
  -> SDK starts Firecracker
  -> sample Firecracker host process and cgroup metrics
```

## Host Responsibilities

The host side owns:

- RawTree API key
- provider metadata
- sandbox id and run id
- Firecracker process lifecycle
- Firecracker logger path
- Firecracker metrics path
- Firecracker host PID and cgroup metrics
- event batching and writes to RawTree

The RawTree collector should run next to the Firecracker process, not inside the guest.

## Guest Responsibilities

None for this reference.

This version does not install a RawTree agent, does not change the guest init process, and does not require RawTree credentials inside the VM.

If the provider already has guest-level events from its own sandbox control plane, such as exec, file upload/download, stdout/stderr, or workload logs, those can be emitted to RawTree as provider-native events. They are outside this Firecracker-only reference.

## Firecracker Logger

The provider configures Firecracker's logger through `firecracker-go-sdk`:

```txt
Config.LogPath -> PUT /logger
```

The log path is a per-sandbox host file. Firecracker writes VMM diagnostics there. The RawTree collector turns each log line into `sandbox.firecracker.vmm.log`.

## Firecracker Metrics

The provider configures Firecracker's metrics system through `firecracker-go-sdk`:

```txt
Config.MetricsPath -> PUT /metrics
```

The metrics path is a per-sandbox host file. Firecracker writes JSON metrics there periodically and when the provider calls:

```txt
Client.CreateSyncAction({ action_type: "FlushMetrics" }) -> PUT /actions
```

The RawTree collector turns each metrics JSON object into `sandbox.firecracker.vmm.metrics`.

## Hypervisor Samples

The reference samples the Firecracker process from the host:

```txt
/proc/<firecracker-pid>/stat
/proc/<firecracker-pid>/status
/proc/<firecracker-pid>/io
/proc/<firecracker-pid>/fd
/proc/<firecracker-pid>/cgroup
/sys/fs/cgroup/<sandbox-cgroup>/cpu.stat
/sys/fs/cgroup/<sandbox-cgroup>/memory.current
```

The RawTree collector stores those values under a nested `hypervisor` object in `sandbox.hypervisor.sample` events. RawTree exposes those as dotted fields such as `hypervisor.process.status.vm_rss_bytes`, `hypervisor.process.fd_count`, and `hypervisor.cgroup.cpu_stat.usage_usec`. In a production provider platform, this maps to the same pattern as hypervisor/control-plane telemetry: the provider owns CPU, memory, and lifecycle data outside the guest and pushes those samples to RawTree.

For precise per-sandbox cgroup CPU and memory, run each microVM in its own provider-owned cgroup. If the Firecracker process stays in a shared service cgroup, `memory.current` and `cpu.stat` describe that shared cgroup, while `/proc/<pid>/status` and `/proc/<pid>/stat` still describe the Firecracker process itself.

## Security Notes

- Keep RawTree credentials on the host.
- Treat Firecracker logs as operational data that may still contain sensitive paths or configuration.
- Add provider-specific redaction before writing events to RawTree.
- Use per-sandbox output files and collector state.
- Prefer per-sandbox cgroups so CPU and memory samples map cleanly to one sandbox.
- Bound queue sizes and memory usage.
- Decide which provider metadata is safe to attach to every event.
