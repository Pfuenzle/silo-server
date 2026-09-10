import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import RequestPosterCard from "./RequestPosterCard";
import type { RequestMediaResult } from "@/api/types";

const requestable: RequestMediaResult = {
  media_type: "movie",
  tmdb_id: 42,
  title: "Test Movie",
  availability: "missing",
  request: { requestable: true },
};

describe("RequestPosterCard (discover variant)", () => {
  it("renders the hover Request button when onRequest is provided", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <RequestPosterCard
          variant="discover"
          item={requestable}
          isSubmitting={false}
          onRequest={() => {}}
        />
      </MemoryRouter>,
    );
    // Must render an actual <button> with the "Request" label, not just any "Request"
    // substring (the /requests/... URL would match a naive includes check).
    expect(markup).toMatch(/<button[^>]*>[\s\S]*?Request[\s\S]*?<\/button>/);
  });

  it("contains the hover overlay inside the poster frame", () => {
    render(
      <MemoryRouter>
        <RequestPosterCard
          variant="discover"
          item={requestable}
          isSubmitting={false}
          onRequest={() => {}}
        />
      </MemoryRouter>,
    );

    const overlay = screen.getByTestId("request-poster-hover-overlay");
    const posterFrame = overlay.closest(".media-card-image");

    expect(posterFrame).not.toBeNull();
  });

  it("does not render the hover Request button when onRequest is omitted", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <RequestPosterCard variant="discover" item={requestable} />
      </MemoryRouter>,
    );

    // The discover variant only contains one <button> (the hover Request action);
    // its absence is the strongest signal that the button was suppressed.
    expect(markup).not.toContain("<button");
  });

  it("shows the media type so same-title movies and series stay distinguishable", () => {
    const movieMarkup = renderToStaticMarkup(
      <MemoryRouter>
        <RequestPosterCard variant="discover" item={requestable} />
      </MemoryRouter>,
    );
    const seriesMarkup = renderToStaticMarkup(
      <MemoryRouter>
        <RequestPosterCard
          variant="discover"
          item={{ ...requestable, media_type: "series", tmdb_id: 43 }}
        />
      </MemoryRouter>,
    );

    expect(movieMarkup).toContain(">Movie<");
    expect(seriesMarkup).toContain(">Series<");
  });

  it("shows Audiobook for audiobook media", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <RequestPosterCard
          variant="discover"
          item={{ ...requestable, media_type: "audiobook", tmdb_id: 44 }}
        />
      </MemoryRouter>,
    );

    expect(markup).toContain("<span>Audiobook</span>");
  });

  it("preserves movie and series mine-card routes", () => {
    const request = {
      id: "request-1",
      provider: "tmdb",
      tmdb_id: 42,
      title: "Existing title",
      status: "downloading" as const,
      outcome: "active" as const,
      created_at: "2026-09-09T00:00:00Z",
      updated_at: "2026-09-09T00:00:00Z",
    };
    const movieMarkup = renderToStaticMarkup(
      <MemoryRouter>
        <RequestPosterCard variant="mine" request={{ ...request, media_type: "movie" }} />
      </MemoryRouter>,
    );
    const seriesMarkup = renderToStaticMarkup(
      <MemoryRouter>
        <RequestPosterCard variant="mine" request={{ ...request, media_type: "series" }} />
      </MemoryRouter>,
    );

    expect(movieMarkup).toContain('href="/requests/movie/42"');
    expect(seriesMarkup).toContain('href="/requests/series/42"');
  });
});

describe("RequestPosterCard (audiobook lifecycle)", () => {
  const baseRequest = {
    id: "request-1",
    provider: "audiobook-metadata",
    media_type: "audiobook" as const,
    tmdb_id: 0,
    title: "A Book",
    status: "downloading" as const,
    outcome: "active" as const,
    created_at: "2026-09-09T00:00:00Z",
    updated_at: "2026-09-09T00:00:00Z",
    external_detail: null,
    external_library_id: null,
    external_download_id: null,
    external_status: null,
    silo_audiobook_link: null,
  };

  it.each([
    ["queued", "Queued"],
    ["downloading", "Downloading"],
    ["imported", "Imported"],
    ["scanning", "Scanning"],
    ["completed", "Completed"],
    ["failed", "Failed"],
  ] as const)("renders the %s external state", (externalStatus, label) => {
    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <RequestPosterCard
          variant="mine"
          request={{ ...baseRequest, external_status: externalStatus }}
        />
      </MemoryRouter>,
    );

    expect(markup).toContain(`>${label}<`);
  });

  it("links a completed audiobook to its Silo item route", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <RequestPosterCard
          variant="mine"
          request={{
            ...baseRequest,
            status: "completed",
            external_status: "completed",
            silo_audiobook_link: "/api/v1/items/silo-book-1",
          }}
        />
      </MemoryRouter>,
    );

    expect(markup).toContain('href="/item/silo-book-1"');
    expect(markup).toContain("Ready to listen");
  });

  it("renders the available Listenarr reference", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <RequestPosterCard
          variant="mine"
          request={{ ...baseRequest, external_download_id: "download-42" }}
        />
      </MemoryRouter>,
    );

    expect(markup).toContain("Listenarr reference: download-42");
  });

  it("does not call a request completed when the Silo link is missing", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <RequestPosterCard
          variant="mine"
          request={{ ...baseRequest, status: "completed", external_status: "completed" }}
        />
      </MemoryRouter>,
    );

    expect(markup).toContain("Silo library link is not available yet");
    expect(markup).not.toContain("Ready to listen");
  });

  it("renders untrusted failure detail as text without raw HTML", () => {
    const detail = '<img src=x onerror="alert(1)"> provider failed';
    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <RequestPosterCard
          variant="mine"
          request={{ ...baseRequest, external_status: "failed", external_detail: detail }}
        />
      </MemoryRouter>,
    );

    expect(markup).toContain("&lt;img");
    expect(markup).not.toContain('<img src=x');
  });

  it("renders actionable guidance for a failed payload with null diagnostics", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <RequestPosterCard
          variant="mine"
          request={{
            ...baseRequest,
            outcome: "failed",
            external_status: null,
            external_detail: null,
            last_error: null,
          }}
        />
      </MemoryRouter>,
    );

    expect(markup).toContain("Retry the request or contact an administrator.");
  });
});
