export const LIVE_TV_TABS = ["favorites", "program", "channels"] as const;
export type LiveTVTab = (typeof LIVE_TV_TABS)[number];
export const LIVE_TV_VIEWS = ["grid", "list"] as const;
export type LiveTVView = (typeof LIVE_TV_VIEWS)[number];

const LEGACY_TAB_LABELS: Record<string, LiveTVTab> = {
  Favoriten: "favorites",
  Programm: "program",
  "Alle Sender": "channels",
  Favorites: "favorites",
  Program: "program",
  "All Channels": "channels",
};

export function parseLiveTVSearchParams(params: URLSearchParams): {
  readonly tab: LiveTVTab;
  readonly view: LiveTVView;
} {
  const requestedTab = params.get("tab");
  const requestedView = params.get("view");
  return {
    tab: LIVE_TV_TABS.includes(requestedTab as LiveTVTab)
      ? (requestedTab as LiveTVTab)
      : (LEGACY_TAB_LABELS[requestedTab ?? ""] ?? "channels"),
    view: LIVE_TV_VIEWS.includes(requestedView as LiveTVView)
      ? (requestedView as LiveTVView)
      : "grid",
  };
}

export function updateLiveTVSearchParams(
  current: URLSearchParams,
  next: { readonly tab: LiveTVTab; readonly view: LiveTVView },
): URLSearchParams {
  const params = new URLSearchParams(current);
  params.set("tab", next.tab);
  params.set("view", next.view);
  return params;
}
