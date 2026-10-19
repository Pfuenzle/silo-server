import { afterEach, describe, expect, it, vi } from "vitest";
import listMyRequestsOk from "../../../../contracts/api/v2/fixtures/list_my_requests_ok.json";
import { v2 } from "./request";
import {
  createMediaRequestV2,
  getRequestMediaDetailV2,
  listMyMediaRequestsV2,
  searchRequestMediaV2,
} from "./requests";
import type { components } from "./schema";

vi.mock("./request", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./request")>()),
  v2: vi.fn(),
}));

type Schemas = components["schemas"];

afterEach(() => vi.resetAllMocks());

// The adapters rebuild each body, so a field they leave out never reaches a
// page. Download progress must survive every one of them.
describe("request v2 adapters keep download progress", () => {
  const download: Schemas["RequestDownload"] = {
    phase: "downloading",
    percent: 43,
    bytes_total: 4294967296,
    bytes_left: 2448131358,
    estimated_completion_at: "2026-01-02T03:16:05.678Z",
    downloads: 1,
    updated_at: "2026-01-02T03:04:05.678Z",
  };

  it("on a request and on each of its servers", async () => {
    vi.mocked(v2).mockResolvedValue(listMyRequestsOk as never);

    const [waiting, downloading] = await listMyMediaRequestsV2();

    expect(downloading!.download).toEqual(download);
    expect(downloading!.targets![0]!.download).toEqual(download);
    expect(waiting!.download).toBeUndefined();
    expect(waiting!.targets![0]).not.toHaveProperty("download");
  });

  it("on the title detail's request state", async () => {
    const detail: Schemas["RequestMediaDetail"] = {
      media_type: "movie",
      tmdb_id: 949,
      title: "Heat",
      availability: "missing",
      in_watchlist: false,
      cast: [],
      creators: [],
      genres: [],
      networks: [],
      production_companies: [],
      recommendations: [],
      seasons: [],
      request: {
        requestable: false,
        following: true,
        requested_by_viewer: true,
        status: "downloading",
        state: "processing",
        request_id: "r-1",
        download,
      },
    };
    vi.mocked(v2).mockResolvedValue(detail as never);

    const mapped = await getRequestMediaDetailV2("movie", 949);

    expect(mapped.request.download).toEqual(download);
  });
});

describe("audiobook v2 request lifecycle", () => {
  it("searches and creates with the metadata provider identity and retains library lifecycle fields", async () => {
    const audiobook = {
      media_type: "audiobook" as const,
      provider: "audiobook-metadata",
      provider_item_id: "edition-123",
      tmdb_id: 0,
      title: "Dune",
      availability: "missing",
      request: { requestable: true },
    };
    vi.mocked(v2).mockResolvedValueOnce({
      page: 1,
      total_pages: 1,
      total_results: 1,
      results: [audiobook],
    } as never);
    const page = await searchRequestMediaV2("audiobook", "dune", 1);
    expect(v2).toHaveBeenLastCalledWith("GET /api/v2/requests/search", {
      query: { q: "dune", media_type: "audiobook", page: 1 },
      signal: undefined,
    });
    expect(page.results[0]).toEqual(audiobook);

    const created = {
      id: "request-book",
      ...audiobook,
      status: "completed",
      outcome: "active",
      state: "available",
      targets: [],
      silo_audiobook_id: "book-123",
      silo_audiobook_link: "/item/book-123",
      retryable: false,
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-02T00:00:00Z",
    };
    vi.mocked(v2).mockResolvedValueOnce(created as never);
    const input = {
      media_type: "audiobook" as const,
      provider: "audiobook-metadata",
      provider_item_id: "edition-123",
      title: "Dune",
    };
    const request = await createMediaRequestV2(input);

    expect(v2).toHaveBeenLastCalledWith("POST /api/v2/requests", { body: input });
    expect(request).toMatchObject({
      provider: "audiobook-metadata",
      provider_item_id: "edition-123",
      media_type: "audiobook",
      silo_audiobook_id: "book-123",
      silo_audiobook_link: "/item/book-123",
      retryable: false,
    });

    vi.mocked(v2).mockResolvedValueOnce({ items: [created] } as never);
    const mine = await listMyMediaRequestsV2();
    expect(mine[0]).toMatchObject({
      provider_item_id: "edition-123",
      silo_audiobook_id: "book-123",
      silo_audiobook_link: "/item/book-123",
      retryable: false,
    });
  });
});
