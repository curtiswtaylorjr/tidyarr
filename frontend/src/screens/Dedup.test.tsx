// Stage 3 Dedup UI tests — the staged scan→propose→apply DEDUPLICATION queue,
// per mode, plus the bounded bulk-apply exception.
//
// Dedup is structurally different from Rename/Purge: a proposal is a GROUP of
// candidate files, and Apply carries a body ({keepIndex} or {keepAll}) picking
// which file to keep. These tests pin the two correctness traps a stubbed fetch
// is the only place they CAN'T be caught end-to-end but CAN be pinned at the
// body-shape level:
//   1. keepIndex is the array index of the SELECTED radio, in received order —
//      re-picking a non-winner sends that candidate's index, not the winner's.
//   2. keepIndex === 0 must still be sent (falsy-guard trap) — picking the
//      index-0 candidate when it isn't the winner sends {keepIndex: 0}, never an
//      empty body (which would let the backend delete the operator's keeper).
//
// Bulk apply (a deliberate, documented reversal of the original
// no-apply-everything rule — see ROADMAP.md and the top-level CLAUDE.md) adds an
// opt-in multi-select of Pending groups, applied in ONE apply-batch. Its own
// correctness trap: a batched group sends keepIndex ONLY when the operator
// changed that group's radio (an explicit keepSel override) — otherwise the item
// omits keepIndex so the backend keeps its own auto-winner. The bulk tests pin
// that per-item shape plus: checkboxes only on Pending cards, "Apply Selected"
// only with a non-empty selection, one apply-batch (not N single applies), and
// selection-clears-after-batch.
//
// Covered: Movies apply-one (default winner index), Series re-pick a NON-winner
// (keepIndex = chosen index), the index-0 pick, Keep All ({keepAll: true}, no
// keepIndex), Dismiss, Scan→refetch, bulk apply (checkbox gating, per-item
// keepIndex-only-on-override, one batch call, selection clears), and Adult
// per-mode endpoint wiring.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import type { Candidate, Proposal } from "@dto";
import { Dedup } from "./Dedup";

// MockEventSource mirrors Dashboard.test.tsx / BrowserNotifications.test.tsx:
// jsdom has no EventSource, and Dedup now opens one on mount (the scan-progress
// stream), so it MUST be stubbed for every test in this file — not just the new
// streaming ones. The most recently constructed instance is captured so a test
// can fire scan frames at the active mode's stream.
class MockEventSource {
  static last: MockEventSource | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  onerror: ((ev: Event) => void) | null = null;
  url: string;
  closed = false;

  constructor(url: string) {
    this.url = url;
    MockEventSource.last = this;
  }

  close() {
    this.closed = true;
  }

  // emit fires a scan frame the way the real SSE onmessage path does: the server
  // sends `data: {"type":...}`, so the frame's `data` string is the JSON-encoded
  // dedupscan.Event.
  emit(frame: unknown) {
    this.onmessage?.({ data: JSON.stringify(frame) } as MessageEvent);
  }
}

const jsonResponse = (obj: unknown): Response =>
  new Response(JSON.stringify(obj), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });

const noContent = (): Response => new Response(null, { status: 204 });

// accepted mirrors the scan POST's new 202 (no body) contract.
const accepted = (): Response => new Response(null, { status: 202 });

beforeEach(() => {
  MockEventSource.last = null;
  vi.stubGlobal("EventSource", MockEventSource);
});

const candidate = (over: Partial<Candidate>): Candidate => ({
  label: "file.mkv",
  path: "/movies/file.mkv",
  resolution: 1080,
  codec: "h264",
  bitRate: 8_000_000,
  winner: false,
  ...over,
});

// A two-candidate group: index 0 tracked (winner by default), index 1 orphan.
const dedupProposal = (over: Partial<Proposal>): Proposal => ({
  id: 1,
  status: "pending",
  sourceName: "Some Movie",
  rootFolderPath: "/movies",
  title: "Some Movie",
  reason: "2 copies identified as \"Some Movie\"",
  candidates: [
    candidate({ label: "tracked", path: "/movies/keep.mkv", winner: true }),
    candidate({ label: "orphan.mkv", path: "/movies/dupe.mkv", winner: false }),
  ],
  ...over,
});

type Call = { url: string; method: string; body: unknown };
type Handler = (url: string, init?: RequestInit) => Response | Promise<Response>;

const stubFetch = (handler: Handler) => {
  const calls: Call[] = [];
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push({
      url,
      method: (init?.method ?? "GET").toUpperCase(),
      body: init?.body ? JSON.parse(init.body as string) : undefined,
    });
    return handler(url, init);
  });
  vi.stubGlobal("fetch", fn);
  return calls;
};

const applyCalls = (calls: Call[]) =>
  calls.filter((c) => c.url.includes("/apply"));

// batchCalls / singleApplyCalls disambiguate the two apply routes: "/apply-batch"
// also matches ".includes('/apply')", so bulk tests match "/apply-batch"
// precisely and exclude it when counting single-item applies.
const batchCalls = (calls: Call[]) =>
  calls.filter((c) => c.url.includes("/apply-batch"));
const singleApplyCalls = (calls: Call[]) =>
  calls.filter(
    (c) => c.url.includes("/apply") && !c.url.includes("/apply-batch"),
  );

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("Dedup — Movies (scan → propose → apply the pre-selected winner)", () => {
  it("applies exactly one group, keeping the flagged winner by default", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([dedupProposal({ id: 7, title: "Dupe Group" })]);
      if (
        url.includes("/api/proposals/7/apply") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    expect(await screen.findByText("Dupe Group")).toBeInTheDocument();
    // Both candidate rows render, in wire order.
    expect(screen.getByText("tracked")).toBeInTheDocument();
    expect(screen.getByText("orphan.mkv")).toBeInTheDocument();

    fireEvent.click(screen.getByText("Apply"));

    await waitFor(() => expect(applyCalls(calls)).toHaveLength(1));
    const call = applyCalls(calls)[0]!;
    expect(call.url).toContain("/api/proposals/7/apply");
    expect(call.method).toBe("POST");
    // Winner is candidate index 0 → default keepIndex 0 (sent, not dropped).
    expect(call.body).toEqual({ keepIndex: 0 });
  });

  it("kicks off a scan (202 POST) then re-fetches the queue on the done frame", async () => {
    // The scan is now async: the POST returns 202 and the queue only refetches
    // when the SSE `done` frame arrives — NOT right after the POST resolves.
    let done = false;
    const calls = stubFetch((url, init) => {
      if (
        url.includes("/api/modes/movies/dedup/scan") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return accepted();
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse(
          done ? [dedupProposal({ id: 1, title: "Found After Scan" })] : [],
        );
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    expect(
      await screen.findByText(/No duplicate groups yet/),
    ).toBeInTheDocument();

    fireEvent.click(screen.getByText("Scan"));
    // The POST fired, the button flips to Scanning…, and the stale empty-state
    // text is gone — but the queue has NOT refetched yet (no done frame).
    expect(
      calls.some((c) => c.url.includes("/dedup/scan") && c.method === "POST"),
    ).toBe(true);
    await waitFor(() =>
      expect(screen.queryByText(/No duplicate groups yet/)).toBeNull(),
    );
    expect(screen.getByText("Scanning…")).toBeInTheDocument();

    // The done frame drives the refetch that repopulates the queue.
    done = true;
    MockEventSource.last!.emit({ type: "done", mode: "movies", count: 1, total: 0 });
    expect(await screen.findByText("Found After Scan")).toBeInTheDocument();
    // Scanning cleared: the button returns to its idle label.
    expect(screen.getByText("Scan")).toBeInTheDocument();
  });
});

describe("Dedup — Series (re-pick a non-winner keeper)", () => {
  it("sends the SELECTED candidate's index, not the winner's", async () => {
    // Winner is index 0; the operator re-picks index 1 (the orphan). The Apply
    // body must carry keepIndex 1, or the backend would delete the file the
    // operator chose to keep.
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/series/dedup/proposals"))
        return jsonResponse([
          dedupProposal({
            id: 9,
            title: "Show S01E02",
            candidates: [
              candidate({ label: "tracked", path: "/tv/keep.mkv", winner: true }),
              candidate({ label: "better.mkv", path: "/tv/better.mkv", winner: false }),
            ],
          }),
        ]);
      if (
        url.includes("/api/proposals/9/apply") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    fireEvent.click(await screen.findByText("Series"));
    await screen.findByText("Show S01E02");

    // Pick the non-winner keeper (index 1).
    fireEvent.click(screen.getByLabelText("Keep better.mkv"));
    fireEvent.click(screen.getByText("Apply"));

    await waitFor(() => expect(applyCalls(calls)).toHaveLength(1));
    expect(applyCalls(calls)[0]!.body).toEqual({ keepIndex: 1 });
  });

  it("sends keepIndex 0 (not an empty body) when index-0 is picked but isn't the winner", async () => {
    // Falsy-guard trap: winner sits at index 1, operator explicitly picks the
    // index-0 candidate. The body MUST be { keepIndex: 0 } — an empty body would
    // let the backend fall back to its auto-winner (index 1) and delete the
    // operator's keeper.
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/series/dedup/proposals"))
        return jsonResponse([
          dedupProposal({
            id: 11,
            title: "Zero Pick",
            candidates: [
              candidate({ label: "first.mkv", path: "/tv/first.mkv", winner: false }),
              candidate({ label: "tracked", path: "/tv/keep.mkv", winner: true }),
            ],
          }),
        ]);
      if (
        url.includes("/api/proposals/11/apply") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    fireEvent.click(await screen.findByText("Series"));
    await screen.findByText("Zero Pick");

    fireEvent.click(screen.getByLabelText("Keep first.mkv"));
    fireEvent.click(screen.getByText("Apply"));

    await waitFor(() => expect(applyCalls(calls)).toHaveLength(1));
    const body = applyCalls(calls)[0]!.body as Record<string, unknown>;
    expect(body).toEqual({ keepIndex: 0 });
    expect("keepIndex" in body).toBe(true);
  });
});

describe("Dedup — Keep All and Dismiss", () => {
  it("Keep All sends keepAll:true with no keepIndex", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([dedupProposal({ id: 3, title: "Keep Both" })]);
      if (
        url.includes("/api/proposals/3/apply") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText("Keep Both");
    fireEvent.click(screen.getByText("Keep All"));

    await waitFor(() => expect(applyCalls(calls)).toHaveLength(1));
    const body = applyCalls(calls)[0]!.body as Record<string, unknown>;
    expect(body).toEqual({ keepAll: true });
    expect("keepIndex" in body).toBe(false);
  });

  it("Dismiss drops exactly one group without applying", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([dedupProposal({ id: 4, title: "Dismiss Me" })]);
      if (
        url.includes("/api/proposals/4/dismiss") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText("Dismiss Me");
    fireEvent.click(screen.getByText("Dismiss"));

    await waitFor(() =>
      expect(
        calls.some((c) => c.url.includes("/api/proposals/4/dismiss")),
      ).toBe(true),
    );
    // Dismiss is not an apply.
    expect(applyCalls(calls)).toHaveLength(0);
  });
});

describe("Dedup — single-group apply is still one group per click", () => {
  it("resolves exactly the clicked group's proposal id, per-group Apply/Keep All/Dismiss present", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([
          dedupProposal({ id: 1, title: "A" }),
          dedupProposal({ id: 2, title: "B" }),
          dedupProposal({ id: 3, title: "C" }),
        ]);
      if (
        url.includes("/api/proposals/2/apply") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText("A");

    // Each group keeps its own Apply / Keep All / Dismiss controls.
    expect(screen.getAllByText("Apply")).toHaveLength(3);
    expect(screen.getAllByText("Keep All")).toHaveLength(3);
    expect(screen.getAllByText("Dismiss")).toHaveLength(3);

    // Clicking one group's Apply resolves exactly that one proposal id — one
    // single-item apply, no batch.
    fireEvent.click(screen.getAllByText("Apply")[1]!);
    await waitFor(() => expect(singleApplyCalls(calls)).toHaveLength(1));
    expect(singleApplyCalls(calls)[0]!.url).toContain("/api/proposals/2/apply");
    expect(batchCalls(calls)).toHaveLength(0);
  });
});

describe("Dedup — bulk apply (opt-in multi-select of Pending groups)", () => {
  it("renders a checkbox only for Pending cards, never for a non-pending one", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([
          dedupProposal({ id: 1, title: "Pending One", status: "pending" }),
          dedupProposal({ id: 2, title: "Pending Two", status: "pending" }),
          dedupProposal({ id: 3, title: "Done Group", status: "applied" }),
        ]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText("Pending One");

    expect(screen.getByLabelText("Select Pending One")).toBeInTheDocument();
    expect(screen.getByLabelText("Select Pending Two")).toBeInTheDocument();
    expect(screen.queryByLabelText("Select Done Group")).toBeNull();
    // Two pending card checkboxes + one select-all checkbox = 3. (Keepers are
    // radios, not checkboxes, so they don't count here.)
    expect(document.querySelectorAll('input[type="checkbox"]')).toHaveLength(3);
  });

  it("shows 'Apply Selected' only once a group is selected", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([dedupProposal({ id: 1, title: "Only" })]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText("Only");

    expect(screen.queryByText(/Apply Selected/)).toBeNull();
    fireEvent.click(screen.getByLabelText("Select Only"));
    expect(await screen.findByText("Apply Selected (1)")).toBeInTheDocument();
  });

  it("sends ONE apply-batch, with keepIndex only for a group whose radio was overridden, then clears the selection", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([
          dedupProposal({
            id: 1,
            title: "Group One",
            candidates: [
              candidate({ label: "one-keep", winner: true }),
              candidate({ label: "one-dupe", winner: false }),
            ],
          }),
          dedupProposal({
            id: 2,
            title: "Group Two",
            candidates: [
              candidate({ label: "two-keep", winner: true }),
              candidate({ label: "two-better", winner: false }),
            ],
          }),
        ]);
      if (
        url.includes("/api/proposals/apply-batch") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return jsonResponse({
          results: [
            { id: 1, ok: true },
            { id: 2, ok: true },
          ],
        });
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText("Group One");

    // Select all pending; override only Group Two's keeper radio (index 1).
    fireEvent.click(screen.getByLabelText("Select all pending"));
    fireEvent.click(screen.getByLabelText("Keep two-better"));
    fireEvent.click(await screen.findByText("Apply Selected (2)"));

    await waitFor(() => expect(batchCalls(calls)).toHaveLength(1));
    expect(singleApplyCalls(calls)).toHaveLength(0);
    // Group One kept its auto-winner → no keepIndex; Group Two overridden → 1.
    expect(batchCalls(calls)[0]!.body).toEqual({
      items: [{ id: 1 }, { id: 2, keepIndex: 1 }],
    });
    expect(await screen.findByText("2 applied, 0 failed")).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByText(/Apply Selected/)).toBeNull());
  });
});

describe("Dedup — Adult (per-mode endpoint wiring)", () => {
  it("targets the adult dedup endpoints when the Adult tab is active", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([]); // Movies renders first; keep it quiet.
      if (url.includes("/api/modes/adult/dedup/proposals"))
        return jsonResponse([
          dedupProposal({
            id: 21,
            title: "Studio - Scene",
            candidates: [
              candidate({ label: "tracked", path: "/adult/keep.mp4", winner: true }),
              candidate({ label: "dupe.mp4", path: "/adult/dupe.mp4", winner: false }),
            ],
          }),
        ]);
      if (
        url.includes("/api/proposals/21/apply") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    fireEvent.click(await screen.findByText("Adult"));
    await screen.findByText("Studio - Scene");
    fireEvent.click(screen.getByText("Apply"));

    await waitFor(() => expect(applyCalls(calls)).toHaveLength(1));
    expect(applyCalls(calls)[0]!.url).toContain("/api/proposals/21/apply");
    expect(
      calls.some((c) => c.url.includes("/api/modes/adult/dedup/proposals")),
    ).toBe(true);
  });
});

describe("Dedup — live scan progress stream", () => {
  it("populates the log box on progress and clears + refetches on done, empty-state hidden while scanning", async () => {
    let done = false;
    stubFetch((url, init) => {
      if (
        url.includes("/api/modes/movies/dedup/scan") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return accepted();
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse(
          done ? [dedupProposal({ id: 1, title: "Resolved Dupe" })] : [],
        );
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText(/No duplicate groups yet/);

    fireEvent.click(screen.getByText("Scan"));
    const es = MockEventSource.last!;

    // A progress frame appears as a "current/total · name · phase" log line, and
    // the stale "No duplicate groups yet" text is gone while scanning.
    es.emit({
      type: "progress",
      mode: "movies",
      current: 1,
      total: 3,
      name: "a.mkv",
      phase: "hashing",
    });
    expect(await screen.findByText(/1\/3 · a\.mkv · hashing/)).toBeInTheDocument();
    expect(screen.queryByText(/No duplicate groups yet/)).toBeNull();

    es.emit({
      type: "progress",
      mode: "movies",
      current: 2,
      total: 3,
      name: "b.mkv",
      phase: "hashing",
    });
    expect(await screen.findByText(/2\/3 · b\.mkv · hashing/)).toBeInTheDocument();

    // The done frame clears scanning, clears the log box, and refetches the
    // resolved proposal list.
    done = true;
    es.emit({ type: "done", mode: "movies", count: 1, total: 3 });
    expect(await screen.findByText("Resolved Dupe")).toBeInTheDocument();
    // Log lines are gone (box cleared) and the button is idle again.
    expect(screen.queryByText(/2\/3 · b\.mkv/)).toBeNull();
    expect(screen.getByText("Scan")).toBeInTheDocument();
  });

  it("renders a neutral 'Starting scan…' state for the synthetic starting seed (no 0/0 log row)", async () => {
    stubFetch((url, init) => {
      if (
        url.includes("/api/modes/movies/dedup/scan") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return accepted();
      if (url.includes("/api/modes/movies/dedup/proposals")) return jsonResponse([]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText(/No duplicate groups yet/);
    fireEvent.click(screen.getByText("Scan"));

    // The reconnect-priming seed carries phase:"starting" with current/total
    // ABSENT (omitempty) — it must show "Starting scan…", never a "0/0" row.
    MockEventSource.last!.emit({ type: "progress", mode: "movies", phase: "starting" });
    expect(await screen.findByText(/Starting scan/)).toBeInTheDocument();
    expect(screen.queryByText(/0\/0/)).toBeNull();
  });

  it("surfaces an error frame and clears scanning — the UI is not left stuck", async () => {
    stubFetch((url, init) => {
      if (
        url.includes("/api/modes/movies/dedup/scan") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return accepted();
      if (url.includes("/api/modes/movies/dedup/proposals")) return jsonResponse([]);
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText(/No duplicate groups yet/);
    fireEvent.click(screen.getByText("Scan"));
    expect(screen.getByText("Scanning…")).toBeInTheDocument();

    MockEventSource.last!.emit({
      type: "error",
      mode: "movies",
      error: "root folder unreadable",
    });
    expect(
      await screen.findByText(/root folder unreadable/),
    ).toBeInTheDocument();
    // Scanning cleared — button is idle, not stuck on "Scanning…".
    expect(screen.getByText("Scan")).toBeInTheDocument();
  });

  it("recovers a stuck scan via the liveness backstop when the terminal frame is DROPPED", async () => {
    // The scan runs but NO terminal frame is ever delivered on the stream (it was
    // dropped/delayed). The quiet-window timer must fire, reconcile against the
    // status endpoint (inflight:false ⇒ the scan really finished), and clear
    // scanning + refetch — proving the terminal state is recoverable without a
    // page reload even when the terminal SSE frame itself is lost.
    let recovered = false;
    let statusChecks = 0;
    stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/dedup/scan/status")) {
        statusChecks++;
        return jsonResponse({ inflight: false });
      }
      if (
        url.includes("/api/modes/movies/dedup/scan") &&
        (init?.method ?? "").toUpperCase() === "POST"
      )
        return accepted();
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse(
          recovered ? [dedupProposal({ id: 9, title: "Recovered Group" })] : [],
        );
      throw new Error("unexpected fetch: " + url);
    });

    // Real timers for the initial mount/resource; fake timers only for the
    // 15s quiet window (armed inside initiate below).
    render(() => <Dedup />);
    await screen.findByText(/No duplicate groups yet/);

    vi.useFakeTimers();
    try {
      // The proposals refetch that the backstop triggers must now return groups.
      recovered = true;
      fireEvent.click(screen.getByText("Scan"));
      expect(screen.getByText("Scanning…")).toBeInTheDocument();

      // No progress and no done/error frame is ever emitted. Advancing past the
      // quiet window fires the backstop, which reconciles via the status endpoint
      // and recovers. advanceTimersByTimeAsync also flushes the reconcile's async
      // status-fetch + refetch microtasks.
      await vi.advanceTimersByTimeAsync(15_000);
      expect(statusChecks).toBeGreaterThan(0);
    } finally {
      vi.useRealTimers();
    }

    // Scanning cleared and the queue repopulated purely from the status reconcile
    // — the terminal frame was never delivered.
    expect(await screen.findByText("Recovered Group")).toBeInTheDocument();
    expect(screen.getByText("Scan")).toBeInTheDocument();
  });
});
