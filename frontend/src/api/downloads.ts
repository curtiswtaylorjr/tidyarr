// Unified downloader data access. The live queue (active + waiting + recent
// stopped) plus per-item pause/resume/cancel, backed by the aria2c engine SAK
// manages. Every call goes through api() (src/api/client.ts) so it inherits the
// session cookie and the global 401 → re-boot session-expiry fallback.
// Request/response shapes are the generated DTOs (@dto), never hand-duplicated.

import { api } from "./client";
import type { Download } from "@dto";

export type { Download };

// fetchDownloads lists the current merged queue (active + waiting + recent
// stopped). The Downloads screen uses the SSE stream for live updates; this is
// the one-shot fallback / initial paint helper.
export function fetchDownloads(): Promise<Download[]> {
  return api<Download[]>("/api/downloads");
}

// cancelDownload removes a download and clears its stopped-list result — a true
// "remove it entirely" for the queue UI.
export function cancelDownload(gid: string): Promise<void> {
  return api<void>(`/api/downloads/${encodeURIComponent(gid)}`, {
    method: "DELETE",
  });
}

// pauseDownload pauses an active download.
export function pauseDownload(gid: string): Promise<void> {
  return api<void>(`/api/downloads/${encodeURIComponent(gid)}/pause`, {
    method: "POST",
  });
}

// resumeDownload unpauses a paused download.
export function resumeDownload(gid: string): Promise<void> {
  return api<void>(`/api/downloads/${encodeURIComponent(gid)}/resume`, {
    method: "POST",
  });
}
