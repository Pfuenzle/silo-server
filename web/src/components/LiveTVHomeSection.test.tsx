// @vitest-environment jsdom

import { render, screen } from "@testing-library/react";
import type { ReactElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";
import { MemoryRouter } from "react-router";

import { LiveTVHomeSection } from "./LiveTVHomeSection";
import { liveTVHomeSectionsSchema } from "@/api/livetv";

vi.mock("@/components/MediaCarousel", () => ({
  default: ({ title, children }: { readonly title: string; readonly children: ReactElement[] }) => (
    <section className="media-carousel" data-title={title}>
      <h2>{title}</h2>
      {children}
    </section>
  ),
}));

const programme = (id: string, title: string, rating?: number) => ({
  id,
  channel_id: "news|1",
  channel_name: "News Network",
  title,
  starts_at: "2026-09-11T10:00:00Z",
  ends_at: "2026-09-11T11:00:00Z",
  ...(rating === undefined ? {} : { rating }),
});

describe("LiveTVHomeSection", () => {
  const renderHome = (element: ReactElement) =>
    render(
      <QueryClientProvider client={new QueryClient()}>
        <MemoryRouter>{element}</MemoryRouter>
      </QueryClientProvider>,
    );

  it("renders currently airing as a horizontal media row with sender and timing", () => {
    renderHome(
      <LiveTVHomeSection
        libraryId={7}
        query={{
          data: {
            currently_airing: [programme("current", "Morning News")],
            favorite_channels_currently_airing: [{ id: "news|1", name: "News" }],
            favorite_programmes_currently_airing: [programme("favorite", "Favorite Show")],
            top_rated_favorite_programmes: [programme("rated", "Top Show", 4.8)],
            upcoming_favorite_programmes: [programme("upcoming", "Tonight")],
          },
          isLoading: false,
          isFetching: true,
          isError: false,
          refetch: vi.fn(),
        }}
        locale="en"
      />,
    );

    expect(screen.getByRole("heading", { name: "Currently airing" })).toBeVisible();
    expect(screen.getByTestId("live-tv-currently-airing-row")).toContainElement(
      document.querySelector(".media-carousel"),
    );
    expect(screen.getByText("News Network")).toBeVisible();
    expect(
      screen.getByTestId("live-tv-currently-airing-row").querySelector("p.mt-2"),
    ).toHaveTextContent("10:00 AM – 11:00 AM");
    expect(screen.getByRole("link", { name: /Watch live: Morning News/ })).toHaveAttribute(
      "href",
      "/library/7?tab=program",
    );
    expect(screen.getByRole("heading", { name: "Favorite channels airing" })).toBeVisible();
    expect(screen.getByRole("heading", { name: "Top-rated favorites" })).toBeVisible();
    expect(screen.getByRole("heading", { name: "Upcoming favorites" })).toBeVisible();
    expect(screen.getByRole("status")).toHaveTextContent("Live TV data may be out of date.");
  });

  it("keeps currently airing before the other Live TV rails", () => {
    renderHome(
      <LiveTVHomeSection
        libraryId={7}
        query={{
          data: {
            currently_airing: [programme("current", "Morning News")],
            favorite_channels_currently_airing: [{ id: "news|1", name: "News" }],
            favorite_programmes_currently_airing: [programme("favorite", "Favorite Show")],
            top_rated_favorite_programmes: [programme("rated", "Top Show", 4.8)],
            upcoming_favorite_programmes: [programme("upcoming", "Tonight")],
          },
          isLoading: false,
          isFetching: false,
          isError: false,
          refetch: vi.fn(),
        }}
      />,
    );

    const rows = screen.getAllByRole("heading").map((heading) => heading.textContent);
    expect(rows.indexOf("Currently airing")).toBeLessThan(rows.indexOf("Favorite channels airing"));
  });

  it("rejects malformed home-section payloads at the API boundary", () => {
    expect(() => liveTVHomeSectionsSchema.parse({ currently_airing: [] })).toThrow();
  });

  it("handles loading, empty, and error states with localized strings", () => {
    const { rerender } = render(
      <QueryClientProvider client={new QueryClient()}>
        <LiveTVHomeSection
          query={{
            data: undefined,
            isLoading: true,
            isFetching: true,
            isError: false,
            refetch: vi.fn(),
          }}
          locale="de"
        />
      </QueryClientProvider>,
    );
    expect(screen.getByText("Live TV wird geladen…")).toBeVisible();

    rerender(
      <QueryClientProvider client={new QueryClient()}>
        <LiveTVHomeSection
          query={{
            data: {
              currently_airing: [],
              favorite_channels_currently_airing: [],
              favorite_programmes_currently_airing: [],
              top_rated_favorite_programmes: [],
              upcoming_favorite_programmes: [],
            },
            isLoading: false,
            isFetching: false,
            isError: false,
            refetch: vi.fn(),
          }}
          locale="de"
        />
      </QueryClientProvider>,
    );
    expect(screen.getByText("Keine Live-TV-Programme verfügbar.")).toBeVisible();

    rerender(
      <QueryClientProvider client={new QueryClient()}>
        <LiveTVHomeSection
          query={{
            data: undefined,
            isLoading: false,
            isFetching: false,
            isError: true,
            refetch: vi.fn(),
          }}
          locale="de"
        />
      </QueryClientProvider>,
    );
    expect(screen.getByRole("alert")).toHaveTextContent("Live TV konnte nicht geladen werden.");
  });
});
