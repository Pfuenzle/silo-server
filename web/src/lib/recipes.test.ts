import { describe, it, expect, vi, beforeEach } from "vitest";
import {
  fetchRecipeCatalog,
  fetchCandidates,
  previewSection,
  recipeAvailableForLibraries,
  filterRecipeCatalog,
} from "./recipes";

beforeEach(() => {
  vi.spyOn(globalThis, "fetch").mockReset();
});

describe("recipes API client", () => {
  it("omits a Live TV recipe when no Live TV library is available", () => {
    const definition = {
      type: "currently_airing",
      category: "library_staples" as const,
      required_library_type: "livetv",
      presets: [],
      avoid_duplicates: false,
      supports_rotation: false,
      admin_only: false,
    };

    expect(recipeAvailableForLibraries(definition, [{ type: "movies" }])).toBe(false);
    expect(recipeAvailableForLibraries(definition, [{ type: "livetv" }])).toBe(true);
  });

  it("filters recipe catalogs by required library type", () => {
    const catalog = {
      categories: {
        library_staples: [
        {
          type: "currently_airing",
          category: "library_staples" as const,
          required_library_type: "livetv",
          presets: [],
          avoid_duplicates: false,
          supports_rotation: false,
          admin_only: false,
        },
        {
          type: "recently_added",
          category: "library_staples" as const,
          presets: [],
          avoid_duplicates: false,
          supports_rotation: false,
          admin_only: false,
        },
        ],
      },
    };

    expect(
      filterRecipeCatalog(catalog, [{ type: "movies" }]).categories.library_staples,
    ).toHaveLength(1);
    expect(
      filterRecipeCatalog(catalog, [{ type: "livetv" }]).categories.library_staples,
    ).toHaveLength(2);
  });

  it("fetchRecipeCatalog returns categories", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue({
      ok: true,
      status: 200,
      text: async () =>
        JSON.stringify({ categories: { library_staples: [{ type: "recently_added" }] } }),
    } as Response);

    const res = await fetchRecipeCatalog();
    expect(res.categories.library_staples?.[0]!.type).toBe("recently_added");
  });

  it("fetchCandidates returns candidate list", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue({
      ok: true,
      status: 200,
      text: async () =>
        JSON.stringify({
          candidates: [{ value: "action", display_name: "Action" }],
        }),
    } as Response);

    const candidates = await fetchCandidates("genre");
    expect(candidates[0]!.value).toBe("action");
  });

  it("previewSection POSTs body and returns items", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue({
      ok: true,
      status: 200,
      text: async () => JSON.stringify({ items: [{ content_id: "x" }], total_count: 1 }),
    } as Response);

    const res = await previewSection({
      section_type: "recently_added",
      config: {},
      item_limit: 10,
    });
    expect(res.total_count).toBe(1);
  });
});
