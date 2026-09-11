// @vitest-environment jsdom

import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { api } from "@/api/client";
import { LiveTVSourceEditor } from "./LiveTVSourceEditor";

vi.mock("@/api/client", () => ({ api: vi.fn() }));
vi.mock("@/hooks/queries/livetv", () => ({
  useLiveTVSources: () => ({
    data: {
      items: [
        {
          id: 4,
          library_id: 9,
          kind: "playlist",
          source_key: "main",
          name: "Main playlist",
          enabled: true,
          refresh_state: "ready",
        },
      ],
    },
    isFetching: false,
    refetch: vi.fn(),
  }),
}));
vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: { language: "en" } }),
}));

function renderEditor() {
  return render(
    <QueryClientProvider client={new QueryClient()}>
      <LiveTVSourceEditor libraryId={9} />
    </QueryClientProvider>,
  );
}

describe("LiveTVSourceEditor", () => {
  beforeEach(() => {
    vi.mocked(api).mockReset();
  });

  it("edits a configured source with a PUT without exposing its provider URL", async () => {
    vi.mocked(api).mockResolvedValue({});
    renderEditor();

    fireEvent.click(screen.getByRole("button", { name: /edit source main playlist/i }));
    expect(screen.getByLabelText("Server URL")).toHaveValue("");
    expect(screen.getByLabelText("Source key")).toHaveAttribute("readonly");
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Updated playlist" } });
    fireEvent.change(screen.getByLabelText("Server URL"), {
      target: { value: "https://provider.example/playlist.m3u" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save sources" }));

    await waitFor(() =>
      expect(api).toHaveBeenCalledWith(
        "/livetv/libraries/9/sources/main",
        expect.objectContaining({ method: "PUT" }),
      ),
    );
    const calls = vi.mocked(api).mock.calls;
    expect(calls[calls.length - 1]?.[1]).toEqual(
      expect.objectContaining({
        body: JSON.stringify({
          kind: "playlist",
          source_key: "main",
          name: "Updated playlist",
          location: "https://provider.example/playlist.m3u",
          enabled: true,
        }),
      }),
    );
    expect(screen.getByRole("status")).toHaveTextContent("Sources saved.");
  });

  it("blocks an invalid configured-source edit before the PUT", async () => {
    renderEditor();
    fireEvent.click(screen.getByRole("button", { name: /edit source main playlist/i }));
    fireEvent.change(screen.getByLabelText("Server URL"), { target: { value: "ftp://provider" } });
    fireEvent.click(screen.getByRole("button", { name: "Save sources" }));

    expect(await screen.findByText("Use an HTTP or HTTPS source URL.")).toBeVisible();
    expect(api).not.toHaveBeenCalled();
  });

  it("preserves the configured URL when editing only the source name", async () => {
    vi.mocked(api).mockResolvedValue({});
    renderEditor();

    fireEvent.click(screen.getByRole("button", { name: /edit source main playlist/i }));
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Renamed playlist" } });
    fireEvent.click(screen.getByRole("button", { name: "Save sources" }));

    await waitFor(() =>
      expect(api).toHaveBeenCalledWith(
        "/livetv/libraries/9/sources/main",
        expect.objectContaining({
          method: "PUT",
          body: JSON.stringify({
            kind: "playlist",
            source_key: "main",
            name: "Renamed playlist",
            location: "",
            enabled: true,
          }),
        }),
      ),
    );
  });
});
