import { describe, expect, it } from "vitest";
import type { MediaRequest } from "@/api/types";
import { audiobookRequestDisplay } from "./mediaRequests";

const request = (overrides: Partial<MediaRequest> = {}): MediaRequest => ({
  id: "request-1",
  provider: "audiobook-metadata",
  media_type: "audiobook",
  tmdb_id: 0,
  title: "A Book",
  status: "completed",
  outcome: "active",
  created_at: "2026-09-09T00:00:00Z",
  updated_at: "2026-09-09T00:00:00Z",
  ...overrides,
});

describe("audiobookRequestDisplay", () => {
  it("prefers fresh external progress over a stale cached Silo status", () => {
    // Given a cached completed status and a current external download state.
    const audiobook = request({ status: "completed", external_status: "downloading" });

    // When the display state is derived.
    const display = audiobookRequestDisplay(audiobook);

    // Then the user sees the current external state, not the stale cache.
    expect(display).toMatchObject({ label: "Downloading", isCompleted: false, isFailed: false });
  });

  it("rejects malformed or external completion links", () => {
    // Given malformed payloads that claim completion without a local Silo route.
    const malformed = request({
      external_status: "completed",
      silo_audiobook_link: "https://attacker.example/items/book-1",
    });

    // When the display state is derived.
    const display = audiobookRequestDisplay(malformed);

    // Then completion is withheld and the failure is actionable.
    expect(display.isCompleted).toBe(false);
    expect(display.isFailed).toBe(true);
    expect(display.detail).toContain("Silo library link is not available yet");
    expect(display.href).toBeNull();
  });

  it.each([
    ["declined", "Declined"],
    ["cancelled", "Cancelled"],
  ] as const)("keeps %s requests terminal despite external completion", (outcome, label) => {
    const display = audiobookRequestDisplay(
      request({
        outcome,
        external_status: "completed",
        silo_audiobook_link: "/api/v1/items/book-1",
      }),
    );

    expect(display).toMatchObject({ label, isCompleted: false, isFailed: true, href: null });
  });

  it("provides actionable guidance when a failed payload has no detail", () => {
    // Given the exact failed API payload with both diagnostic fields null.
    const failed = request({
      outcome: "failed",
      external_status: null,
      external_detail: null,
      last_error: null,
    });

    // When the display state is derived.
    const display = audiobookRequestDisplay(failed);

    // Then the failure still has actionable guidance.
    expect(display.detail).toBe(
      "Listenarr reported a failure. Retry the request or contact an administrator.",
    );
  });
});
