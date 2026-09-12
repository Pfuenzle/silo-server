import { describe, expect, it } from "vitest";
import {
  buildAdminSectionPayload,
  buildProfileSectionSaveEntry,
  filterSectionRecipeCatalog,
  sectionTypeAvailable,
} from "./SectionEditorDrawer";
import { queryDefinitionFromSectionConfig } from "@/api/types";

describe("SectionEditorDrawer payload builders", () => {
  it("hides currently airing from the generic editor without Live TV", () => {
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
        ],
      },
    };

    expect(
      filterSectionRecipeCatalog(catalog, [{ type: "movies" }]).categories.library_staples,
    ).toEqual([]);
    expect(
      filterSectionRecipeCatalog(catalog, [{ type: "livetv" }]).categories.library_staples,
    ).toHaveLength(1);
  });

  it("does not allow a stale currently airing selection after Live TV removal", () => {
    expect(sectionTypeAvailable("currently_airing", [{ type: "movies" }])).toBe(false);
    expect(sectionTypeAvailable("currently_airing", [{ type: "livetv" }])).toBe(true);
    expect(sectionTypeAvailable("recently_added", [{ type: "movies" }])).toBe(true);
  });

  it("preserves continue listening config for admin sections", () => {
    const payload = buildAdminSectionPayload({
      section: null,
      scope: "home",
      currentLibraryId: null,
      sectionType: "continue_watching",
      title: "Continue Listening",
      itemLimit: 20,
      featured: false,
      enabled: true,
      queryDefinition: queryDefinitionFromSectionConfig(),
      selectedCollectionId: "",
      recipeParams: { continue_type: "listening" },
    });

    expect(payload).toMatchObject({
      section_type: "continue_watching",
      title: "Continue Listening",
      config: { continue_type: "listening" },
    });
  });

  it("preserves continue listening config for profile sections", () => {
    const entry = buildProfileSectionSaveEntry({
      section: null,
      sectionType: "continue_watching",
      title: "Continue Listening",
      itemLimit: 20,
      featured: false,
      queryDefinition: queryDefinitionFromSectionConfig(),
      selectedCollectionId: "",
      recipeParams: { continue_type: "listening" },
    });

    expect(entry).toMatchObject({
      section_type: "continue_watching",
      title: "Continue Listening",
      is_custom: true,
      config: { continue_type: "listening" },
    });
  });
});
