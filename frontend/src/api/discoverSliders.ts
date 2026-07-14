// Discover-sliders data access — admin-defined custom Discover rows (Seerr's
// CreateSlider/DiscoverSliderEdit equivalent), task #4/#5's backend. Paths
// and the Slider DTO were confirmed directly against worker-1's committed
// internal/api/discover_sliders.go and internal/apidto/ts/dto.gen.ts (task
// #5, via `git show` against their branch) — no longer a guess.

import { api } from "./client";
import type { DiscoverItem, Slider } from "@dto";

export type { Slider };

// FilterType/Target narrow Slider's plain `string` filterType/target fields
// to the fixed enums internal/discoversliders.FilterType/Target actually
// emit — the same shared-narrowing pattern ProposalStatus applies to
// Proposal.status (src/api/discover.ts), kept out of apidto so the
// generated DTO stays a minimal wire mirror.
export type FilterType =
  | "genre"
  | "keyword"
  | "studio"
  | "network"
  | "upcoming"
  | "trending"
  | "popular";

export type Target = "movie" | "tv" | "mixed";

// fetchDiscoverSliders lists every admin-defined slider, already ordered by
// sortOrder (discoversliders.Store.List's own ordering) — GET
// /api/discover/sliders.
export function fetchDiscoverSliders(): Promise<Slider[]> {
  return api<Slider[]>("/api/discover/sliders");
}

// fetchSliderItems resolves one slider's actual TMDB items for the given
// 1-based page — GET /api/discover/sliders/{id}/resolve. A "mixed"-target
// slider's items are already movie-then-tv concatenated server-side
// (resolveSlider's own doc comment), so this returns one flat page exactly
// like fetchDiscover's category rows do.
export function fetchSliderItems(
  sliderId: number,
  page = 1,
): Promise<DiscoverItem[]> {
  return api<DiscoverItem[]>(
    `/api/discover/sliders/${sliderId}/resolve?page=${page}`,
  );
}
