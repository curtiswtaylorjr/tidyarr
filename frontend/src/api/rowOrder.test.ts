// mergeRowOrder tests — the pure reconciliation function Mainstream.tsx/
// Adult.tsx (via useRowOrder) apply on every render: keep stored order for
// still-known keys, drop dead ones, append new ones. See its doc comment
// for the full contract this locks down.

import { describe, expect, it } from "vitest";
import { mergeRowOrder } from "./rowOrder";

describe("mergeRowOrder", () => {
  it("returns knownKeys as-is when nothing is stored yet (fresh install default order)", () => {
    expect(mergeRowOrder([], ["a", "b", "c"])).toEqual(["a", "b", "c"]);
  });

  it("preserves the stored relative order for keys that are still known", () => {
    expect(mergeRowOrder(["c", "a", "b"], ["a", "b", "c"])).toEqual([
      "c",
      "a",
      "b",
    ]);
  });

  it("drops a stored key that no longer resolves to anything live (a deleted slider/feed)", () => {
    expect(mergeRowOrder(["a", "deleted", "b"], ["a", "b"])).toEqual([
      "a",
      "b",
    ]);
  });

  it("appends a known key absent from the stored order, in knownKeys' own order", () => {
    expect(mergeRowOrder(["b"], ["a", "b", "c"])).toEqual(["b", "a", "c"]);
  });

  it("handles a fully stale stored order (every stored key now dead)", () => {
    expect(mergeRowOrder(["gone1", "gone2"], ["a", "b"])).toEqual(["a", "b"]);
  });
});
