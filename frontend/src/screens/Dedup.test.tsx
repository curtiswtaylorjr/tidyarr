// Stage 3 Dedup UI tests — the staged scan→propose→apply DEDUPLICATION queue,
// per mode, plus the explicit no-bulk-action assertion (Acceptance Criterion 6).
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
// Covered: Movies apply-one (default winner index), Series re-pick a NON-winner
// (keepIndex = chosen index), the index-0 pick, Keep All ({keepAll: true}, no
// keepIndex), Dismiss, Scan→refetch, the no-bulk invariant (one Apply per group,
// no apply-all/resolve-all/select-all, no checkboxes, one proposal id per click),
// and Adult per-mode endpoint wiring.

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import type { Candidate, Proposal } from "@dto";
import { Dedup } from "./Dedup";

const jsonResponse = (obj: unknown): Response =>
  new Response(JSON.stringify(obj), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });

const noContent = (): Response => new Response(null, { status: 204 });

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

  it("triggers a scan then re-fetches the queue on the Scan button", async () => {
    let scanned = false;
    const calls = stubFetch((url, init) => {
      if (
        url.includes("/api/modes/movies/dedup/scan") &&
        (init?.method ?? "").toUpperCase() === "POST"
      ) {
        scanned = true;
        return noContent();
      }
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse(
          scanned ? [dedupProposal({ id: 1, title: "Found After Scan" })] : [],
        );
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    expect(await screen.findByText(/No duplicate groups yet/)).toBeInTheDocument();
    fireEvent.click(screen.getByText("Scan"));
    expect(await screen.findByText("Found After Scan")).toBeInTheDocument();
    expect(
      calls.some((c) => c.url.includes("/dedup/scan") && c.method === "POST"),
    ).toBe(true);
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

describe("Dedup — no bulk actions (Acceptance Criterion 6)", () => {
  it("renders one Apply per group and no queue-wide resolve-all affordance", async () => {
    const calls = stubFetch((url, init) => {
      if (url.includes("/api/modes/movies/dedup/proposals"))
        return jsonResponse([
          dedupProposal({ id: 1, title: "A" }),
          dedupProposal({ id: 2, title: "B" }),
          dedupProposal({ id: 3, title: "C" }),
        ]);
      if (url.includes("/apply") && (init?.method ?? "").toUpperCase() === "POST")
        return noContent();
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Dedup />);
    await screen.findByText("A");

    // One Apply / Keep All / Dismiss per group — never a single batch control.
    expect(screen.getAllByText("Apply")).toHaveLength(3);
    expect(screen.getAllByText("Keep All")).toHaveLength(3);
    expect(screen.getAllByText("Dismiss")).toHaveLength(3);
    expect(screen.queryByText(/apply all/i)).toBeNull();
    expect(screen.queryByText(/resolve all/i)).toBeNull();
    expect(screen.queryByText(/dedup all/i)).toBeNull();
    expect(screen.queryByText(/select all/i)).toBeNull();
    // The keeper is picked with radios, never selection checkboxes.
    expect(document.querySelectorAll('input[type="checkbox"]')).toHaveLength(0);

    // Clicking one group's Apply resolves exactly that one proposal id.
    fireEvent.click(screen.getAllByText("Apply")[1]!);
    await waitFor(() => expect(applyCalls(calls)).toHaveLength(1));
    expect(applyCalls(calls)[0]!.url).toContain("/api/proposals/2/apply");
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
