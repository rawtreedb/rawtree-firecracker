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
  let flushing = false;
  let closed = false;

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
    if (flushing || pending.length === 0) {
      return;
    }

    flushing = true;
    const events = pending.splice(0, pending.length);

    try {
      for (const event of events) {
        await writeRawTreeEvent(event, options.request);
      }
    } catch (error) {
      pending.unshift(...events);
      throw error;
    } finally {
      flushing = false;
    }
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
}

function enrichEvent(event: RawTreeEvent, request: SandboxLaunchRequest): RawTreeEvent {
  return {
    ...event,
    event_time: event.event_time ?? new Date().toISOString(),
    event_id: event.event_id ?? randomUUID(),
    provider: request.provider,
    rawtree_metadata_json: JSON.stringify(request.metadata),
    run_id: request.runId,
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
