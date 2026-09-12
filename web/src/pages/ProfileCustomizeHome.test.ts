import { describe, expect, it } from "vitest";

import { reorderProfileSections, type ProfileSection } from "./ProfileCustomizeHome";

function section(id: string, sectionType: string): ProfileSection {
  return { id, is_custom: false, section_type: sectionType, title: id, hidden: false };
}

describe("reorderProfileSections", () => {
  it("moves the currently airing row and preserves the complete persisted order", () => {
    const sections = [
      section("continue", "continue_watching"),
      section("live-tv", "currently_airing"),
      section("recent", "recently_added"),
    ];

    const reordered = reorderProfileSections(sections, "live-tv", -1);

    expect(reordered.map((item) => item.id)).toEqual(["live-tv", "continue", "recent"]);
  });

  it("does not move the first row above the home list", () => {
    const sections = [section("live-tv", "currently_airing"), section("recent", "recently_added")];

    expect(reorderProfileSections(sections, "live-tv", -1)).toEqual(sections);
  });
});
