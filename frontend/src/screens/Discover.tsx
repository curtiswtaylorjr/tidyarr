// Discover — the read-only browse landing (Stage 1 Wave 3). Seerr-inspired
// look: a hero banner plus horizontal category rows (NOT a flat grid) for
// Movies/Series, and a scene grid for Adult. Poster/scene art is rendered
// ONLY through the image proxy (src/api/discover.ts's proxyImage/tmdbPoster),
// never hot-linked from TMDB/TPDB (plan Decision #7).
//
// READ-ONLY by design: a card shows detail (title/year/rating/overview) and an
// availability badge, but there is NO grab/request affordance — clicking a card
// mutates nothing. Auto-grab is Stage 2's job; adding any grab button here
// would violate this wave's scope and the project's no-bulk / staged-approval
// invariants.

import {
  type Component,
  createResource,
  createSignal,
  For,
  Show,
  Switch,
  Match,
} from "solid-js";
import {
  type AdultDiscoverItem,
  type AvailabilityResponse,
  type DiscoverItem,
  type Mode,
  fetchAdultAvailability,
  fetchAdultDiscover,
  fetchDiscover,
  fetchTitleAvailability,
  proxyImage,
  tmdbHero,
  tmdbPoster,
} from "../api/discover";
import { ErrorText, Muted } from "../components/ui";

const MODES: { id: Mode; label: string }[] = [
  { id: "movies", label: "Movies" },
  { id: "series", label: "Series" },
  { id: "adult", label: "Adult" },
];

// year pulls the leading 4-digit year from a TMDB/TPDB date string ("YYYY-..").
function year(date: string): string {
  return date && date.length >= 4 ? date.slice(0, 4) : "";
}

// AvailabilityBadge renders the outcome of a per-card availability probe. It is
// deliberately quiet on failure: Prowlarr may not be configured (a 400/502),
// which must not break the card — it just shows no badge. Loading shows a
// neutral pill so the grid doesn't jump.
const AvailabilityBadge: Component<{
  result: AvailabilityResponse | null | undefined;
  loading: boolean;
}> = (props) => (
  <Show
    when={!props.loading}
    fallback={
      <span class="inline-block rounded-full bg-surface-2 px-2 py-0.5 text-[11px] text-muted">
        checking…
      </span>
    }
  >
    <Show when={props.result}>
      {(r) => (
        <span
          class="inline-block rounded-full px-2 py-0.5 text-[11px] font-medium"
          classList={{
            "bg-ok/20 text-ok": r().available,
            "bg-surface-2 text-muted": !r().available,
          }}
        >
          {r().available ? `${r().releaseCount} available` : "no release"}
        </span>
      )}
    </Show>
  </Show>
);

// TextPoster is the fallback tile when no art exists (TMDB/TPDB returned a
// blank poster/image) — a titled placeholder that keeps the card's footprint
// identical to an image card so rows don't reflow.
const TextPoster: Component<{ label: string }> = (props) => (
  <div class="flex h-full w-full items-center justify-center bg-surface-2 p-2 text-center text-xs text-muted">
    {props.label}
  </div>
);

// PosterCard is one Movies/Series title. Fixed width so a row scrolls
// horizontally. The title attribute carries the overview as a native tooltip —
// "show more detail" without any click handler that could mutate.
const PosterCard: Component<{ mode: "movies" | "series"; item: DiscoverItem }> = (
  props,
) => {
  const [avail] = createResource(
    () => props.item.id,
    (id) => fetchTitleAvailability(props.mode, id).catch(() => null),
  );
  const src = () => tmdbPoster(props.item.posterPath);
  return (
    <div class="w-36 shrink-0" title={props.item.overview}>
      <div class="aspect-[2/3] overflow-hidden rounded-lg border border-border bg-surface">
        <Show when={src()} fallback={<TextPoster label={props.item.title} />}>
          <img
            src={src()}
            alt={props.item.title}
            loading="lazy"
            class="h-full w-full object-cover"
          />
        </Show>
      </div>
      <div class="mt-1.5 truncate text-sm text-fg" title={props.item.title}>
        {props.item.title}
      </div>
      <div class="flex items-center gap-2 text-xs text-muted">
        <span>{year(props.item.releaseDate) || "—"}</span>
        <Show when={props.item.voteAverage > 0}>
          <span>★ {props.item.voteAverage.toFixed(1)}</span>
        </Show>
      </div>
      <div class="mt-1">
        <AvailabilityBadge result={avail()} loading={avail.loading} />
      </div>
    </div>
  );
};

// Row is one horizontal, scrollable category strip.
const Row: Component<{
  title: string;
  mode: "movies" | "series";
  items: DiscoverItem[] | undefined;
  loading: boolean;
}> = (props) => (
  <section class="mt-6">
    <h2 class="mb-2 text-sm font-semibold uppercase tracking-wide text-muted">
      {props.title}
    </h2>
    <Show
      when={!props.loading}
      fallback={<Muted>Loading…</Muted>}
    >
      <Show when={props.items && props.items.length > 0} fallback={<Muted>Nothing here yet.</Muted>}>
        <div class="flex gap-3 overflow-x-auto pb-2">
          <For each={props.items}>
            {(item) => <PosterCard mode={props.mode} item={item} />}
          </For>
        </div>
      </Show>
    </Show>
  </section>
);

// Hero is the top trending title, rendered wide with its backdrop/poster and
// overview — the Seerr-style banner. Falls back to a plain heading if no art.
const Hero: Component<{ item: DiscoverItem | undefined }> = (props) => (
  <Show when={props.item}>
    {(item) => {
      const src = () => tmdbHero(item().posterPath);
      return (
        <div class="relative overflow-hidden rounded-xl border border-border bg-surface">
          <Show when={src()}>
            <img
              src={src()}
              alt={item().title}
              class="absolute inset-0 h-full w-full object-cover opacity-30"
            />
          </Show>
          <div class="relative max-w-2xl p-6">
            <h1 class="text-2xl font-semibold text-fg">{item().title}</h1>
            <div class="mt-1 flex items-center gap-3 text-sm text-muted">
              <span>{year(item().releaseDate)}</span>
              <Show when={item().voteAverage > 0}>
                <span>★ {item().voteAverage.toFixed(1)}</span>
              </Show>
            </div>
            <p class="mt-3 line-clamp-3 text-sm text-muted">{item().overview}</p>
          </div>
        </div>
      );
    }}
  </Show>
);

// TitleDiscover backs Movies and Series (both TMDB title-shaped). Both category
// resources re-run when props.mode changes, so switching tabs refetches.
const TitleDiscover: Component<{ mode: "movies" | "series" }> = (props) => {
  const [trending] = createResource(
    () => props.mode,
    (m) => fetchDiscover(m, "trending"),
  );
  const [popular] = createResource(
    () => props.mode,
    (m) => fetchDiscover(m, "popular"),
  );

  return (
    <div>
      <Show when={trending.error || popular.error}>
        <ErrorText>
          {(trending.error as Error)?.message ??
            (popular.error as Error)?.message}
        </ErrorText>
      </Show>
      <Show when={!trending.loading}>
        <Hero item={trending()?.[0]} />
      </Show>
      <Row
        title="Trending this week"
        mode={props.mode}
        items={trending()}
        loading={trending.loading}
      />
      <Row
        title="Popular"
        mode={props.mode}
        items={popular()}
        loading={popular.loading}
      />
    </div>
  );
};

// AdultCard is one TPDB scene. TPDB frequently returns no art, so the image is
// Show-guarded with a text fallback (the old frontend rendered Adult text-only;
// this adds art where TPDB provides it, via the proxy).
const AdultCard: Component<{ item: AdultDiscoverItem }> = (props) => {
  const [avail] = createResource(
    () => props.item.id,
    () =>
      fetchAdultAvailability(props.item.studio, props.item.title).catch(
        () => null,
      ),
  );
  const src = () => proxyImage(props.item.image);
  const subtitle = () =>
    [props.item.studio, year(props.item.date)].filter(Boolean).join(" · ");
  return (
    <div class="w-40 shrink-0" title={props.item.title}>
      <div class="aspect-video overflow-hidden rounded-lg border border-border bg-surface">
        <Show when={src()} fallback={<TextPoster label={props.item.title} />}>
          <img
            src={src()}
            alt={props.item.title}
            loading="lazy"
            class="h-full w-full object-cover"
          />
        </Show>
      </div>
      <div class="mt-1.5 truncate text-sm text-fg">{props.item.title}</div>
      <div class="truncate text-xs text-muted">{subtitle() || "—"}</div>
      <div class="mt-1">
        <AvailabilityBadge result={avail()} loading={avail.loading} />
      </div>
    </div>
  );
};

// AdultDiscover is the scene-shaped browse: a search box over TPDB's catalog,
// plain paginated browse when the box is empty. No hero (scenes have no single
// "featured" title); a wrapping grid of scene cards.
const AdultDiscover: Component = () => {
  const [submitted, setSubmitted] = createSignal("");
  const [draft, setDraft] = createSignal("");
  const [scenes] = createResource(submitted, (q) => fetchAdultDiscover(q));

  return (
    <div>
      <form
        class="mb-4 flex gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          setSubmitted(draft());
        }}
      >
        <input
          class="w-full max-w-sm rounded-md border border-border bg-bg px-3 py-2 text-sm text-fg outline-none focus:border-accent"
          placeholder="Search scenes by title…"
          value={draft()}
          onInput={(e) => setDraft(e.currentTarget.value)}
        />
      </form>
      <Show when={scenes.error}>
        <ErrorText>{(scenes.error as Error)?.message}</ErrorText>
      </Show>
      <Show when={!scenes.loading} fallback={<Muted>Loading…</Muted>}>
        <Show
          when={scenes() && scenes()!.length > 0}
          fallback={<Muted>No scenes found.</Muted>}
        >
          <div class="flex flex-wrap gap-3">
            <For each={scenes()}>{(item) => <AdultCard item={item} />}</For>
          </div>
        </Show>
      </Show>
    </div>
  );
};

// Discover is the mode-switching shell: tab bar (Movies/Series/Adult) over the
// matching sub-view. Read-only throughout.
export const Discover: Component = () => {
  const [mode, setMode] = createSignal<Mode>("movies");
  return (
    <div>
      <div class="flex gap-1">
        <For each={MODES}>
          {(m) => (
            <button
              type="button"
              class="rounded-md px-3 py-1.5 text-sm font-medium transition"
              classList={{
                "bg-accent text-accent-fg": mode() === m.id,
                "bg-surface-2 text-muted hover:text-fg": mode() !== m.id,
              }}
              onClick={() => setMode(m.id)}
            >
              {m.label}
            </button>
          )}
        </For>
      </div>
      <div class="mt-4">
        <Switch>
          <Match when={mode() === "adult"}>
            <AdultDiscover />
          </Match>
          <Match when={mode() === "movies" || mode() === "series"}>
            <TitleDiscover mode={mode() as "movies" | "series"} />
          </Match>
        </Switch>
      </div>
    </div>
  );
};
