import { describe, expect, it } from "vitest";

import { parseLiveTVSearchParams, updateLiveTVSearchParams } from "./liveTVSearchParams";

describe("Live TV URL state", () => {
  it("parses the localized tabs and defaults the channel view to grid", () => {
    const state = parseLiveTVSearchParams(new URLSearchParams("tab=Programm&view=list"));

    expect(state).toEqual({ tab: "program", view: "list" });
  });

  it("defaults only an invalid tab to All Channels", () => {
    expect(parseLiveTVSearchParams(new URLSearchParams("tab=unknown")).tab).toBe("channels");
    expect(parseLiveTVSearchParams(new URLSearchParams("tab=favorites")).tab).toBe("favorites");
  });

  it("preserves unrelated parameters while persisting tab and view", () => {
    const current = new URLSearchParams("source=guide");
    const next = updateLiveTVSearchParams(current, { tab: "favorites", view: "list" });

    expect(next.toString()).toBe("source=guide&tab=favorites&view=list");
  });

  it("keeps the selected tab when a later view update is based on a fresh URL", () => {
    const current = new URLSearchParams("tab=program&view=grid");

    expect(updateLiveTVSearchParams(current, { tab: "program", view: "list" }).toString()).toBe(
      "tab=program&view=list",
    );
  });

  it("preserves the latest tab when a stale view update is applied", () => {
    const current = new URLSearchParams("tab=favorites&view=grid");

    expect(updateLiveTVSearchParams(current, { tab: "favorites", view: "list" }).toString()).toBe(
      "tab=favorites&view=list",
    );
  });
});
