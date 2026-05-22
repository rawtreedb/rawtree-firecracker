import { randomUUID } from "node:crypto";

import type { RawTreeEvent, SandboxLaunchRequest } from "./types.js";

export type HostCollector = {
  close(): Promise<void>;
  flush(): Promise<void>;
  record(event: RawTreeEvent): Promise<void>;
};

type HostCollectorOptions = {
  request: SandboxLaunchRequest;
};

const FLUSH_INTERVAL_MS = 1000;
const MAX_BATCH_SIZE = 50;

export async function startHostCollector(options: HostCollectorOptions): Promise<HostCollector> {
  const pending: RawTreeEvent[] = [];
  let closed = false;
  let currentFlush: Promise<void> | undefined;

  const interval = setInterval(() => {
    void flush().catch((error) => {
      console.error("RawTree collector flush failed:", error);
    });
  }, FLUSH_INTERVAL_MS);
  interval.unref();

  async function record(event: RawTreeEvent): Promise<void> {
    pending.push(enrichEvent(event, options.request));
    if (pending.length >= MAX_BATCH_SIZE) {
      await flush();
    }
  }

  async function flush(): Promise<void> {
    if (currentFlush) {
      return currentFlush;
    }

    if (pending.length === 0) {
      return;
    }

    currentFlush = drainPending().finally(() => {
      currentFlush = undefined;
    });

    return currentFlush;
  }

  return {
    async close() {
      if (closed) {
        return;
      }

      closed = true;
      clearInterval(interval);
      await flush().catch((error) => {
        console.error("RawTree collector flush failed during close:", error);
      });
    },
    flush,
    record,
  };

  async function drainPending(): Promise<void> {
    while (pending.length > 0) {
      const events = pending.splice(0, pending.length);

      try {
        for (const event of events) {
          await writeRawTreeEvent(event, options.request);
        }
      } catch (error) {
        pending.unshift(...events);
        throw error;
      }
    }
  }
}

function enrichEvent(event: RawTreeEvent, request: SandboxLaunchRequest): RawTreeEvent {
  const eventTime = event.event_time ?? new Date().toISOString();

  return {
    ...event,
    event_time: eventTime,
    event_id: event.event_id ?? randomUUID(),
    metadata: event.metadata ?? request.metadata,
    provider: request.provider,
    run_id: request.runId,
    sampled_at: event.sampled_at ?? eventTime,
    sandbox_id: request.sandboxId,
    source: event.source ?? "firecracker_host_collector",
  };
}

async function writeRawTreeEvent(event: RawTreeEvent, request: SandboxLaunchRequest): Promise<void> {
  const response = await fetch(
    `${request.rawtree.baseUrl.replace(/\/+$/, "")}/v1/tables/${encodeURIComponent(request.rawtree.table)}`,
    {
      body: JSON.stringify(event),
      headers: {
        Authorization: `Bearer ${request.rawtree.apiKey}`,
        "Content-Type": "application/json",
      },
      method: "POST",
    },
  );

  if (!response.ok) {
    const body = await response.text();
    throw new Error(`RawTree insert failed (${response.status}): ${body.slice(0, 500)}`);
  }
}
