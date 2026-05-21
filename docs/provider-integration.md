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
  -> configure Firecracker /logger
  -> configure Firecracker /metrics
  -> configure the normal microVM
  -> start Firecracker
```

## Host Responsibilities

The host side owns:

- RawTree API key
- provider metadata
- sandbox id and run id
- Firecracker process lifecycle
- Firecracker logger path
- Firecracker metrics path
- event batching and writes to RawTree

The RawTree collector should run next to the Firecracker process, not inside the guest.

## Guest Responsibilities

None for this reference.

This version does not install a RawTree agent, does not change the guest init process, and does not require RawTree credentials inside the VM.

If the provider already has guest-level events from its own sandbox control plane, such as exec, file upload/download, stdout/stderr, or workload logs, those can be emitted to RawTree as provider-native events. They are outside this Firecracker-only reference.

## Firecracker Logger

The provider configures Firecracker's logger through the API:

```txt
PUT /logger
```

The log path is a per-sandbox host file. Firecracker writes VMM diagnostics there. The RawTree collector turns each log line into `sandbox.firecracker.vmm.log`.

## Firecracker Metrics

The provider configures Firecracker's metrics system through the API:

```txt
PUT /metrics
```

The metrics path is a per-sandbox host file. Firecracker writes JSON metrics there periodically and when the provider calls:

```txt
PUT /actions { "action_type": "FlushMetrics" }
```

The RawTree collector turns each metrics JSON object into `sandbox.firecracker.vmm.metrics`.

## Security Notes

- Keep RawTree credentials on the host.
- Treat Firecracker logs as operational data that may still contain sensitive paths or configuration.
- Add provider-specific redaction before writing events to RawTree.
- Use per-sandbox output files and collector state.
- Bound queue sizes and memory usage.
- Decide which provider metadata is safe to attach to every event.
