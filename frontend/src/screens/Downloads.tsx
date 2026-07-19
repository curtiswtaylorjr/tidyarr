// Downloads — the unified downloader's live queue (active + waiting + recent
// stopped), fed by the SSE stream at GET /api/downloads/stream (same pattern as
// Dashboard's sysinfo stream). Each event is a JSON array of the current
// downloads. Per item: filename, a progress bar, speed, a status badge, and
// pause/resume/cancel actions. This is NOT a mode-scoped screen (the download
// engine is global, one queue for the whole app), so it registers no mode tabs.
//
// The screen reflects aria2's own queue directly — separate from the per-mode
// Grabs view, which tracks the grab records SAK created. A completed download
// here auto-imports server-side (the downloader's onComplete callback); this
// screen just shows the engine's live state.

import {
  type Component,
  For,
  Show,
  createSignal,
  onCleanup,
  onMount,
} from "solid-js";
import type { Download } from "@dto";
import { cancelDownload, pauseDownload, resumeDownload } from "../api/downloads";
import { Button, ErrorText, Muted } from "../components/ui";

// formatBps renders a bytes/sec value: <1024 → "X B/s", <1MB → "X KB/s",
// else "X.X MB/s" (same scale as Dashboard's formatBps).
function formatBps(bps: number): string {
  if (bps <= 0) return "—";
  if (bps < 1024) return `${Math.round(bps)} B/s`;
  if (bps < 1024 * 1024) return `${Math.round(bps / 1024)} KB/s`;
  return `${(bps / (1024 * 1024)).toFixed(1)} MB/s`;
}

// formatSize renders a byte count as MB/GB for the progress label.
function formatSize(bytes: number): string {
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(0)} MB`;
  return `${(bytes / (1024 * 1024 * 1024)).toFixed(1)} GB`;
}

// STATUS_BADGE maps an aria2 status to a badge color class.
const STATUS_BADGE: Record<string, string> = {
  active: "bg-accent/20 text-accent",
  waiting: "bg-surface-2 text-muted",
  paused: "bg-warn/20 text-warn",
  complete: "bg-ok/20 text-ok",
  error: "bg-danger/20 text-danger",
  removed: "bg-surface-2 text-muted",
};

const ProgressBar: Component<{ percent: number }> = (props) => {
  const clamped = () => Math.max(0, Math.min(100, props.percent));
  return (
    <div class="h-2 w-full overflow-hidden rounded-full bg-surface-2">
      <div
        class="h-full rounded-full bg-accent transition-[width] duration-500"
        style={{ width: `${clamped()}%` }}
      />
    </div>
  );
};

const DownloadRow: Component<{
  dl: Download;
  onAction: (fn: () => Promise<void>) => void;
}> = (props) => {
  const percent = () =>
    props.dl.totalLength > 0
      ? (props.dl.completedLength / props.dl.totalLength) * 100
      : 0;
  const isPaused = () => props.dl.status === "paused";
  const isActive = () => props.dl.status === "active";

  return (
    <li class="flex flex-col gap-2 rounded-md border border-border bg-surface p-3">
      <div class="flex items-center gap-3">
        <div class="min-w-0 flex-1">
          <div class="truncate text-sm text-fg" title={props.dl.filename}>
            {props.dl.filename || props.dl.gid}
          </div>
          <Show when={props.dl.errorMessage}>
            <div class="truncate text-xs text-danger">{props.dl.errorMessage}</div>
          </Show>
        </div>
        <span
          class={`shrink-0 rounded-full px-2 py-0.5 text-[11px] font-medium ${STATUS_BADGE[props.dl.status] ?? "bg-surface-2 text-muted"}`}
        >
          {props.dl.status}
        </span>
      </div>

      <ProgressBar percent={percent()} />

      <div class="flex items-center gap-3 text-xs text-muted">
        <span>
          {formatSize(props.dl.completedLength)} / {formatSize(props.dl.totalLength)}
        </span>
        <Show when={isActive()}>
          <span>{formatBps(props.dl.downloadSpeed)}</span>
          <span>{props.dl.connections} conns</span>
        </Show>
        <div class="ml-auto flex gap-2">
          <Show when={isActive()}>
            <Button onClick={() => props.onAction(() => pauseDownload(props.dl.gid))}>
              Pause
            </Button>
          </Show>
          <Show when={isPaused()}>
            <Button onClick={() => props.onAction(() => resumeDownload(props.dl.gid))}>
              Resume
            </Button>
          </Show>
          <Button onClick={() => props.onAction(() => cancelDownload(props.dl.gid))}>
            {props.dl.status === "complete" || props.dl.status === "error"
              ? "Remove"
              : "Cancel"}
          </Button>
        </div>
      </div>
    </li>
  );
};

export const Downloads: Component = () => {
  const [downloads, setDownloads] = createSignal<Download[]>([]);
  const [reconnecting, setReconnecting] = createSignal(false);
  const [actionError, setActionError] = createSignal<string | null>(null);
  // hasData tracks whether at least one stream frame has arrived, so the empty
  // state ("No active downloads") doesn't flash before the first event.
  const [hasData, setHasData] = createSignal(false);

  let es: EventSource | undefined;

  onMount(() => {
    es = new EventSource("/api/downloads/stream");
    es.onmessage = (ev) => {
      try {
        const list = JSON.parse(ev.data) as Download[];
        setDownloads(list);
        setHasData(true);
        setReconnecting(false);
      } catch {
        /* ignore a malformed frame — the next one should be fine */
      }
    };
    es.onerror = () => setReconnecting(true);
  });

  onCleanup(() => es?.close());

  // runAction fires a mutating call and surfaces its error; the SSE stream
  // reflects the resulting queue change on the next frame, so there's nothing
  // to optimistically update here.
  const runAction = async (fn: () => Promise<void>) => {
    setActionError(null);
    try {
      await fn();
    } catch (err) {
      setActionError((err as Error).message);
    }
  };

  return (
    <div>
      <Show when={reconnecting()}>
        <div class="mb-4 rounded-md border border-warn/40 bg-warn/10 px-3 py-2 text-sm text-warn">
          Connection lost — reconnecting…
        </div>
      </Show>
      <Show when={actionError()}>
        {(msg) => <ErrorText>{msg()}</ErrorText>}
      </Show>

      <Show
        when={hasData()}
        fallback={<Muted>Connecting to the download engine…</Muted>}
      >
        <Show
          when={downloads().length > 0}
          fallback={<Muted>No active downloads</Muted>}
        >
          <ul class="flex flex-col gap-2">
            <For each={downloads()}>
              {(dl) => <DownloadRow dl={dl} onAction={runAction} />}
            </For>
          </ul>
        </Show>
      </Show>
    </div>
  );
};
