// PROVISIONAL — task #5 ("Discover API wiring + sole DTO/codegen owner") has
// not landed yet, so this file's HTTP paths and response shapes are
// hand-written best guesses, NOT generated from internal/apidto. The
// Slider/FilterType/Target field values themselves are not a guess — they
// were verified directly against worker-1's already-committed
// internal/discoversliders/discoversliders.go (read via `git show` against
// their branch), so only the JSON field casing/HTTP paths are speculative,
// not the enum values. Once #5 lands with real internal/apidto/ts/dto.gen.ts
// types and endpoint paths, swap this file's local types for `@dto` imports
// and correct the two fetch paths below — isolated in one file specifically
// so that swap is a small diff, not a rewrite (team-lead direction,
// 2026-07-14).

import { api } from "./client";
import type { DiscoverItem } from "@dto";

// FilterType mirrors internal/discoversliders.FilterType's exact string enum
// values (FilterGenre = "genre", FilterKeyword = "keyword", etc).
export type FilterType =
  | "genre"
  | "keyword"
  | "studio"
  | "network"
  | "upcoming"
  | "trending"
  | "popular";

// Target mirrors internal/discoversliders.Target's exact string enum values.
export type Target = "movie" | "tv" | "mixed";

// Slider mirrors internal/discoversliders.Slider's fields, camelCased to
// match this project's existing generated-DTO convention (DiscoverItem's
// posterPath/voteAverage, etc.) — the casing is an assumption about task
// #5's eventual JSON tags, not confirmed against real wire data.
export type Slider = {
  id: number;
  title: string;
  filterType: FilterType;
  filterValue: string;
  target: Target;
  sortOrder: number;
  enabled: boolean;
};

// fetchDiscoverSliders lists every admin-defined slider, already ordered by
// sortOrder (discoversliders.Store.List's own ordering — see that package's
// `ORDER BY sort_order ASC`). PROVISIONAL path: guessed from this project's
// existing top-level resource convention (e.g. /api/connections).
export function fetchDiscoverSliders(): Promise<Slider[]> {
  return api<Slider[]>("/api/discover-sliders");
}

// fetchSliderItems returns one page of TMDB results for one slider's filter.
// PROVISIONAL path/shape: guessed to mirror fetchDiscover's mode/category/
// page shape (src/api/discover.ts), scoped under the slider's own id instead
// of a mode+category pair, since a slider's target (movie/tv/mixed) already
// encodes what fetchDiscover's `mode` argument would.
export function fetchSliderItems(
  sliderId: number,
  page = 1,
): Promise<DiscoverItem[]> {
  return api<DiscoverItem[]>(
    `/api/discover-sliders/${sliderId}/items?page=${page}`,
  );
}
