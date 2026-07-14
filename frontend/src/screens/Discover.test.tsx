import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@solidjs/testing-library";
import type { AdultDiscoverItem, DiscoverItem, TrackedItem } from "@dto";
import { Discover } from "./Discover";

const jsonResponse = (obj: unknown): Response =>
  new Response(JSON.stringify(obj), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });

const movie = (over: Partial<DiscoverItem>): DiscoverItem => ({
  id: 1,
  title: "Trending Movie",
  posterPath: "/poster1.jpg",
  overview: "An overview.",
  releaseDate: "2024-05-01",
  voteAverage: 7.8,
  mediaType: "movie",
  ...over,
});

const scene = (over: Partial<AdultDiscoverItem>): AdultDiscoverItem => ({
  id: "s1",
  title: "A Scene",
  studio: "Tushy",
  date: "2023-02-02",
  image: "https://cdn.theporndb.net/scenes/abc.jpg",
  durationSeconds: 1800,
  ...over,
});

const tracked = (over: Partial<TrackedItem>): TrackedItem => ({
  id: 10,
  title: "Owned Title",
  tags: [],
  tmdbId: 500,
  year: 2020,
  ...over,
});

type Handler = (url: string) => Response | Promise<Response>;
const stubFetch = (handler: Handler) => {
  const fn = vi.fn(async (input: RequestInfo | URL) => handler(String(input)));
  vi.stubGlobal("fetch", fn);
  return fn;
};

// mainstreamDefaults answers the background fetches the combined Mainstream page
// fires on mount (category rows + the library row's two tracked calls +
// per-card poster probes + TraktWatchlistRow's status check) with empties, so
// each test only has to special-case the calls it actually asserts on.
// Returns null for anything it doesn't recognize, so the caller can fall
// through to its own handler / throw. Trakt defaults to "not linked" so
// TraktWatchlistRow (mounted unconditionally by MainstreamDiscover) stays
// invisible in every test that doesn't explicitly opt into it.
const mainstreamDefaults = (url: string): Response | null => {
  if (url.includes("/discover")) return jsonResponse([]);
  if (url.includes("/tracked")) return jsonResponse([]);
  if (url.includes("/poster")) return jsonResponse({ posterPath: "" });
  if (url.includes("/api/trakt/status"))
    return jsonResponse({ configured: false, linked: false });
  return null;
};

afterEach(() => vi.unstubAllGlobals());

describe("Discover — Mainstream combined rows", () => {
  it("renders all four category rows (movies + series × trending + popular) with cards", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/discover") && url.includes("trending"))
        return jsonResponse([movie({ id: 1, title: "Trend Movie" })]);
      if (url.includes("/api/modes/movies/discover") && url.includes("popular"))
        return jsonResponse([movie({ id: 2, title: "Pop Movie" })]);
      if (url.includes("/api/modes/series/discover") && url.includes("trending"))
        return jsonResponse([movie({ id: 3, title: "Trend Show", mediaType: "tv" })]);
      if (url.includes("/api/modes/series/discover") && url.includes("popular"))
        return jsonResponse([movie({ id: 4, title: "Pop Show", mediaType: "tv" })]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);

    // All four row headers are present (the combined page, not a Movies/Series
    // toggle).
    expect(await screen.findByText("Trending Movies")).toBeInTheDocument();
    expect(screen.getByText("Trending Shows")).toBeInTheDocument();
    expect(screen.getByText("Popular Movies")).toBeInTheDocument();
    expect(screen.getByText("Popular Shows")).toBeInTheDocument();

    // A card from each row renders.
    expect(await screen.findByText("Trend Movie")).toBeInTheDocument();
    expect(await screen.findByText("Trend Show")).toBeInTheDocument();
    expect(await screen.findByText("Pop Movie")).toBeInTheDocument();
    expect(await screen.findByText("Pop Show")).toBeInTheDocument();
  });

  it("routes every poster image through the image proxy — never hot-links image.tmdb.org", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/discover") && url.includes("trending"))
        return jsonResponse([movie({ id: 1, title: "Trend Movie", posterPath: "/p1.jpg" })]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    const { container } = render(() => <Discover />);
    await screen.findByText("Trend Movie");

    const imgs = Array.from(container.querySelectorAll("img"));
    expect(imgs.length).toBeGreaterThan(0);
    for (const img of imgs) {
      const src = img.getAttribute("src") ?? "";
      expect(src.startsWith("/api/images/proxy?url=")).toBe(true);
      expect(src.startsWith("https://image.tmdb.org")).toBe(false);
      expect(decodeURIComponent(src)).toContain("https://image.tmdb.org/t/p/");
    }
  });

  it("falls back to a text tile when a title has no poster", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/discover") && url.includes("trending"))
        return jsonResponse([movie({ id: 1, title: "No Art Movie", posterPath: "" })]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    const { container } = render(() => <Discover />);
    // "No Art Movie" appears twice per card (the text-tile label + the title
    // line), so use findAllByText.
    await screen.findAllByText("No Art Movie");
    // No <img> anywhere (no poster, empty library) — the title still shows via
    // the text tile.
    expect(container.querySelectorAll("img").length).toBe(0);
  });
});

describe("Discover — Upcoming rows (PROVISIONAL, pending task #5)", () => {
  it("renders Upcoming Movies/Upcoming Shows rows with cards from category=upcoming", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/discover") && url.includes("category=upcoming"))
        return jsonResponse([movie({ id: 1, title: "Upcoming Movie" })]);
      if (url.includes("/api/modes/series/discover") && url.includes("category=upcoming"))
        return jsonResponse([movie({ id: 2, title: "Upcoming Show", mediaType: "tv" })]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);

    expect(await screen.findByText("Upcoming Movies")).toBeInTheDocument();
    expect(screen.getByText("Upcoming Shows")).toBeInTheDocument();
    expect(await screen.findByText("Upcoming Movie")).toBeInTheDocument();
    expect(await screen.findByText("Upcoming Show")).toBeInTheDocument();
  });
});

describe("Discover — custom slider rows (PROVISIONAL, pending task #5/#7)", () => {
  it("renders one carousel row per enabled slider, from /api/discover-sliders + its items endpoint", async () => {
    stubFetch((url) => {
      if (url === "/api/discover-sliders") {
        return jsonResponse([
          { id: 1, title: "Heist Movies", filterType: "keyword", filterValue: "heist", target: "movie", sortOrder: 0, enabled: true },
          { id: 2, title: "Disabled Row", filterType: "genre", filterValue: "35", target: "movie", sortOrder: 1, enabled: false },
        ]);
      }
      if (url.includes("/api/discover-sliders/1/items"))
        return jsonResponse([movie({ id: 100, title: "Heist Movie One" })]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);

    expect(await screen.findByText("Heist Movies")).toBeInTheDocument();
    expect(await screen.findByText("Heist Movie One")).toBeInTheDocument();
    // A disabled slider is filtered out client-side — no row, no fetch of its items.
    expect(screen.queryByText("Disabled Row")).not.toBeInTheDocument();
  });

  it("routes a mixed-target slider's per-item grab mode from the item's own mediaType", async () => {
    stubFetch((url) => {
      if (url === "/api/discover-sliders") {
        return jsonResponse([
          { id: 5, title: "Mixed Row", filterType: "trending", filterValue: "", target: "mixed", sortOrder: 0, enabled: true },
        ]);
      }
      if (url.includes("/api/discover-sliders/5/items")) {
        return jsonResponse([
          movie({ id: 200, title: "Mixed Movie Item", mediaType: "movie" }),
          movie({ id: 201, title: "Mixed Show Item", mediaType: "tv" }),
        ]);
      }
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);

    expect(await screen.findByText("Mixed Movie Item")).toBeInTheDocument();
    expect(await screen.findByText("Mixed Show Item")).toBeInTheDocument();

    // The movie card grabs directly (no season/episode picker); the tv card
    // reveals the picker first — same per-item routing LibraryRow/ModedTitle
    // already rely on elsewhere in this file.
    const movieCard = screen
      .getByText("Mixed Movie Item")
      .closest("div.w-36") as HTMLElement;
    fireEvent.click(within(movieCard).getByText("Grab"));
    expect(await screen.findByText(/Grab — Mixed Movie Item/)).toBeInTheDocument();
    fireEvent.click(screen.getByText("Close"));

    const showCard = screen
      .getByText("Mixed Show Item")
      .closest("div.w-36") as HTMLElement;
    fireEvent.click(within(showCard).getByText("Grab"));
    expect(within(showCard).getByLabelText("Season")).toBeInTheDocument();
  });
});

describe("Discover — Carousel lazy-load-more pagination (append, not replace)", () => {
  it("appends the next TMDB page to the row once the carousel scrolls near the end", async () => {
    const fetchMock = stubFetch((url) => {
      if (url.includes("/api/modes/movies/discover") && url.includes("trending")) {
        if (url.includes("page=2"))
          return jsonResponse([movie({ id: 2, title: "Page Two Movie" })]);
        return jsonResponse([movie({ id: 1, title: "Page One Movie" })]);
      }
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    expect(await screen.findByText("Page One Movie")).toBeInTheDocument();

    // The Carousel component's own lazy-load trigger is scroll-position-driven
    // (see components/Carousel.tsx), not a button — jsdom has no real layout
    // engine, so scrollWidth/clientWidth/scrollLeft are stubbed on the row's
    // scroll track to simulate "scrolled near the trailing edge", same
    // approach as components/Carousel.test.tsx.
    const track = screen
      .getByText("Trending Movies")
      .closest("section")!
      .querySelector("div.overflow-x-auto") as HTMLElement;
    Object.defineProperty(track, "scrollWidth", { value: 2000, configurable: true });
    Object.defineProperty(track, "clientWidth", { value: 300, configurable: true });
    Object.defineProperty(track, "scrollLeft", { value: 1700, configurable: true });
    fireEvent.scroll(track);

    // Page two's card appears AND page one's is still present (append).
    expect(await screen.findByText("Page Two Movie")).toBeInTheDocument();
    expect(screen.getByText("Page One Movie")).toBeInTheDocument();

    // The second page was actually requested with page=2.
    expect(
      fetchMock.mock.calls.some(([u]) =>
        String(u).includes("/api/modes/movies/discover") &&
        String(u).includes("trending") &&
        String(u).includes("page=2"),
      ),
    ).toBe(true);
  });
});

describe("Discover — existing-library row", () => {
  it("renders owned movies + series as poster cards with lazily-fetched, proxied art", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/tracked"))
        return jsonResponse([tracked({ id: 10, title: "Owned Movie", tmdbId: 500, year: 2020 })]);
      if (url.includes("/api/modes/series/tracked"))
        return jsonResponse([tracked({ id: 11, title: "Owned Show", tmdbId: 600, year: 2019 })]);
      if (url.includes("/api/modes/movies/poster?tmdbId=500"))
        return jsonResponse({ posterPath: "/libmovie.jpg" });
      if (url.includes("/api/modes/series/poster?tmdbId=600"))
        return jsonResponse({ posterPath: "/libshow.jpg" });
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });
    const { container } = render(() => <Discover />);

    expect(await screen.findByText("In your library")).toBeInTheDocument();
    expect(await screen.findByText("Owned Movie")).toBeInTheDocument();
    expect(await screen.findByText("Owned Show")).toBeInTheDocument();

    // The lazily-resolved library posters render through the proxy.
    const libImgs = Array.from(container.querySelectorAll("img")).filter((img) =>
      decodeURIComponent(img.getAttribute("src") ?? "").match(/libmovie|libshow/),
    );
    expect(libImgs.length).toBe(2);
    for (const img of libImgs) {
      const src = img.getAttribute("src") ?? "";
      expect(src.startsWith("/api/images/proxy?url=")).toBe(true);
      expect(src.startsWith("https://image.tmdb.org")).toBe(false);
    }
  });
});

describe("Discover — Mainstream search (replaces rows, then restores)", () => {
  it("replaces the category rows with merged movie+series results, and restores them on Clear", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/movies/discover") && url.includes("trending"))
        return jsonResponse([movie({ id: 1, title: "A Row Movie" })]);
      if (url.includes("/api/modes/movies/tmdb-search"))
        return jsonResponse([movie({ id: 90, title: "Search Movie" })]);
      if (url.includes("/api/modes/series/tmdb-search"))
        return jsonResponse([movie({ id: 91, title: "Search Show", mediaType: "tv" })]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    // Rows are visible initially.
    expect(await screen.findByText("Trending Movies")).toBeInTheDocument();
    expect(await screen.findByText("A Row Movie")).toBeInTheDocument();

    // Search — the rows are replaced by one merged result grid.
    fireEvent.input(screen.getByPlaceholderText("Search movies & shows…"), {
      target: { value: "search" },
    });
    fireEvent.submit(screen.getByPlaceholderText("Search movies & shows…").closest("form")!);

    expect(await screen.findByText("Search results")).toBeInTheDocument();
    expect(await screen.findByText("Search Movie")).toBeInTheDocument();
    expect(await screen.findByText("Search Show")).toBeInTheDocument();
    // Rows are gone while searching.
    expect(screen.queryByText("Trending Movies")).not.toBeInTheDocument();
    expect(screen.queryByText("A Row Movie")).not.toBeInTheDocument();

    // Clearing restores the rows and drops the search view.
    fireEvent.click(screen.getByText("Clear"));
    expect(await screen.findByText("Trending Movies")).toBeInTheDocument();
    expect(await screen.findByText("A Row Movie")).toBeInTheDocument();
    expect(screen.queryByText("Search results")).not.toBeInTheDocument();
  });
});

describe("Discover — Adult tab (unchanged)", () => {
  it("switches to Adult and renders scene cards with proxied art", async () => {
    stubFetch((url) => {
      if (url.includes("/api/modes/adult/discover"))
        return jsonResponse([scene({ id: "s1", title: "Scene One" })]);
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });
    const { container } = render(() => <Discover />);

    fireEvent.click(await screen.findByText("Adult"));

    expect(await screen.findByText("Scene One")).toBeInTheDocument();
    const imgs = Array.from(container.querySelectorAll("img"));
    expect(imgs.length).toBeGreaterThan(0);
    for (const img of imgs) {
      expect((img.getAttribute("src") ?? "").startsWith("/api/images/proxy?url=")).toBe(true);
    }
  });
});

describe("Discover — TMDB/TPDB not-configured setup pop-up", () => {
  type Call = { url: string; method: string; body: unknown };
  const stubFetchWithCalls = (
    handler: (url: string, init?: RequestInit) => Response | Promise<Response>,
  ) => {
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

  const notConfigured = (service: string) =>
    new Response(`${service} isn't configured yet — add it in Settings first`, {
      status: 400,
    });

  it("shows a setup pop-up (no uncaught error) when TMDB isn't configured", async () => {
    const pageErrors: unknown[] = [];
    const onError = (e: ErrorEvent) => pageErrors.push(e.error ?? e.message);
    window.addEventListener("error", onError);

    stubFetchWithCalls((url) => {
      if (url.includes("/discover")) return notConfigured("tmdb");
      if (url.includes("/tracked")) return jsonResponse([]);
      if (url.includes("/api/trakt/status"))
        return jsonResponse({ configured: false, linked: false });
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);

    expect(await screen.findByText("Set up TMDB")).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: /themoviedb\.org\/settings\/api/i }),
    ).toHaveAttribute("href", "https://www.themoviedb.org/settings/api");
    expect(pageErrors).toHaveLength(0);

    window.removeEventListener("error", onError);
  });

  it("saving an API key from the pop-up PUTs the three-state body, then refetches the rows", async () => {
    let configured = false;
    const calls = stubFetchWithCalls((url, init) => {
      if (url.includes("/api/modes/movies/discover") && url.includes("trending")) {
        return configured
          ? jsonResponse([movie({ id: 1, title: "Now Visible Movie" })])
          : notConfigured("tmdb");
      }
      if (url.includes("/discover")) return configured ? jsonResponse([]) : notConfigured("tmdb");
      if (url.includes("/tracked")) return jsonResponse([]);
      if (url.includes("/api/trakt/status"))
        return jsonResponse({ configured: false, linked: false });
      if (url === "/api/connections/tmdb" && init?.method === "PUT") {
        configured = true;
        return new Response(null, { status: 204 });
      }
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);
    await screen.findByText("Set up TMDB");

    fireEvent.input(screen.getByPlaceholderText("API key"), {
      target: { value: "a-real-tmdb-key" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(await screen.findByText("Now Visible Movie")).toBeInTheDocument();

    const putCall = calls.find(
      (c) => c.url === "/api/connections/tmdb" && c.method === "PUT",
    );
    expect(putCall?.body).toEqual({
      url: "https://api.themoviedb.org/3",
      apiKey: "a-real-tmdb-key",
    });
  });

  it("shows the TPDB pop-up (not TMDB's) when Adult's scene fetch reports tpdb not configured", async () => {
    stubFetchWithCalls((url) => {
      if (url.includes("/api/modes/adult/discover")) return notConfigured("tpdb");
      const d = mainstreamDefaults(url);
      if (d) return d;
      throw new Error("unexpected fetch: " + url);
    });
    render(() => <Discover />);

    fireEvent.click(await screen.findByText("Adult"));

    expect(await screen.findByText("Set up TPDB")).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: /theporndb\.net\/user\/api-tokens/i }),
    ).toHaveAttribute("href", "https://theporndb.net/user/api-tokens");
  });

  it("falls back to plain error text (no pop-up) for an unrelated error", async () => {
    stubFetchWithCalls((url) => {
      if (url.includes("/discover")) return new Response("internal server error", { status: 500 });
      if (url.includes("/tracked")) return jsonResponse([]);
      if (url.includes("/api/trakt/status"))
        return jsonResponse({ configured: false, linked: false });
      throw new Error("unexpected fetch: " + url);
    });

    render(() => <Discover />);

    expect(await screen.findByText("internal server error")).toBeInTheDocument();
    expect(screen.queryByText(/^Set up/)).not.toBeInTheDocument();
  });
});
