import http from "node:http";

import type { FirecrackerConfig } from "./types.js";

export type FirecrackerClient = {
  put(path: string, body: unknown): Promise<void>;
};

export const DEFAULT_BOOT_ARGS = [
  "console=ttyS0",
  "root=/dev/vda",
  "rw",
  "reboot=k",
  "panic=1",
  "pci=off",
].join(" ");

export function firecrackerClient(socketPath: string): FirecrackerClient {
  return {
    async put(requestPath, body) {
      await firecrackerRequest(socketPath, "PUT", requestPath, body);
    },
  };
}

export async function configureFirecrackerMicroVM(
  fc: FirecrackerClient,
  config: FirecrackerConfig,
  rootfsPath: string,
): Promise<void> {
  await fc.put("/machine-config", {
    mem_size_mib: config.memMiB,
    smt: false,
    track_dirty_pages: false,
    vcpu_count: config.vcpuCount,
  });

  await fc.put("/boot-source", {
    boot_args: config.bootArgs ?? DEFAULT_BOOT_ARGS,
    kernel_image_path: config.kernel,
  });

  await fc.put("/drives/rootfs", {
    drive_id: "rootfs",
    is_read_only: false,
    is_root_device: true,
    path_on_host: rootfsPath,
  });

  if (config.tap) {
    await fc.put("/network-interfaces/eth0", {
      guest_mac: config.guestMac ?? "AA:FC:00:00:00:01",
      host_dev_name: config.tap,
      iface_id: "eth0",
    });
  }
}

function firecrackerRequest(
  socketPath: string,
  method: string,
  requestPath: string,
  body: unknown,
): Promise<void> {
  const payload = JSON.stringify(body);

  return new Promise((resolve, reject) => {
    const request = http.request(
      {
        headers: {
          "Content-Length": Buffer.byteLength(payload),
          "Content-Type": "application/json",
        },
        method,
        path: requestPath,
        socketPath,
      },
      (response) => {
        let responseBody = "";
        response.setEncoding("utf8");
        response.on("data", (chunk) => {
          responseBody += chunk;
        });
        response.on("end", () => {
          const statusCode = response.statusCode ?? 0;
          if (statusCode < 200 || statusCode >= 300) {
            reject(new Error(`Firecracker ${method} ${requestPath} failed (${statusCode}): ${responseBody}`));
            return;
          }

          resolve();
        });
      },
    );

    request.on("error", reject);
    request.end(payload);
  });
}
