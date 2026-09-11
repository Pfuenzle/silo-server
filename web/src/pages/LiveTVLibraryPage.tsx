import { useMemo, useState } from "react";
import { Heart, LayoutGrid, List, Radio, Star } from "lucide-react";
import { useSearchParams } from "react-router";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import {
  resolveLiveTVPlayback,
  useLiveTVChannels,
  useLiveTVFavorites,
  useLiveTVGuide,
  useToggleLiveTVFavorite,
} from "@/hooks/queries/livetv";
import { formatTime } from "@/lib/datetime";
import { liveTVT } from "@/lib/i18n";
import { LiveTVPlayer } from "@/player/components/LiveTVPlayer";
import type { LiveTVChannel, LiveTVProgramme } from "@/api/livetv";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import {
  parseLiveTVSearchParams,
  updateLiveTVSearchParams,
  type LiveTVTab,
  type LiveTVView,
} from "./liveTVSearchParams";

function artwork(value: unknown): string | null {
  if (typeof value !== "object" || value === null) return null;
  const candidate = value as { url?: unknown; path?: unknown };
  return typeof candidate.url === "string"
    ? candidate.url
    : typeof candidate.path === "string"
      ? candidate.path
      : null;
}

function Rating({ value }: { readonly value: unknown }) {
  const label =
    typeof value === "number" ? value.toFixed(1) : typeof value === "string" ? value : null;
  return label ? (
    <span className="text-muted-foreground inline-flex items-center gap-1 text-xs">
      <Star className="size-3 fill-current" />
      {label}
    </span>
  ) : (
    <span className="text-muted-foreground text-xs">—</span>
  );
}

function ChannelCard({
  channel,
  favorite,
  onToggle,
  view,
  onPlay,
}: {
  readonly channel: LiveTVChannel;
  readonly favorite: boolean;
  readonly onToggle: () => void;
  readonly view: LiveTVView;
  readonly onPlay: () => void;
}) {
  const image = artwork(channel.artwork);
  return (
    <article
      className={
        view === "list"
          ? "border-border bg-surface flex items-center gap-3 rounded-xl border p-3"
          : "group border-border bg-surface overflow-hidden rounded-xl border"
      }
      data-testid={`live-tv-channel-${channel.id}`}
    >
      <div
        className={
          view === "list"
            ? "bg-muted flex size-14 shrink-0 items-center justify-center overflow-hidden rounded-lg"
            : "bg-muted relative aspect-video w-full"
        }
      >
        {image ? (
          <img src={image} alt="" className="size-full object-cover" width="320" height="180" />
        ) : (
          <Radio className="text-muted-foreground size-7" aria-hidden="true" />
        )}
      </div>
      <div className={view === "list" ? "min-w-0 flex-1" : "p-3"}>
        <div className="flex items-start justify-between gap-2">
          <div className="min-w-0">
            <h3 className="truncate text-sm font-semibold">
              {channel.number ? `${channel.number} · ` : ""}
              {channel.name}
            </h3>
            <p className="text-muted-foreground truncate text-xs">
              {channel.category ?? liveTVT("liveTV")}
            </p>
          </div>
          <button
            type="button"
            className="focus-visible:ring-ring rounded-full p-1 focus-visible:ring-2"
            aria-label={
              favorite
                ? `Remove ${channel.name} from favorites`
                : `Add ${channel.name} to favorites`
            }
            aria-pressed={favorite}
            onClick={onToggle}
          >
            <Heart
              className={
                favorite ? "text-primary size-4 fill-current" : "text-muted-foreground size-4"
              }
            />
          </button>
        </div>
        <div className="mt-2 flex items-center justify-between gap-2">
          <Rating value={channel.rating} />
          <Button type="button" size="sm" onClick={onPlay}>
            Watch live
          </Button>
        </div>
      </div>
    </article>
  );
}

function ProgrammeRow({
  title,
  items,
  favoriteIds,
  onToggle,
  onPlay,
}: {
  readonly title: string;
  readonly items: readonly LiveTVProgramme[];
  readonly favoriteIds?: ReadonlySet<string>;
  readonly onToggle?: (id: string, favorite: boolean) => void;
  readonly onPlay: (item: LiveTVProgramme) => void;
}) {
  if (items.length === 0) return null;
  return (
    <section className="space-y-3" data-testid="live-tv-programme-row">
      <h2 className="text-lg font-semibold">{title}</h2>
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {items.map((item) => (
          <article key={item.id} className="border-border bg-surface rounded-xl border p-3">
            <div className="flex items-center justify-between gap-3">
              <h3 className="truncate text-sm font-medium">{item.title}</h3>
              <div className="flex items-center gap-2">
                <Rating value={item.rating} />
                {favoriteIds && onToggle ? (
                  <button
                    type="button"
                    className="focus-visible:ring-ring rounded-full p-1 focus-visible:ring-2"
                    aria-label={
                      favoriteIds.has(item.id)
                        ? `Remove ${item.title} from favorites`
                        : `Add ${item.title} to favorites`
                    }
                    aria-pressed={favoriteIds.has(item.id)}
                    onClick={() => onToggle(item.id, !favoriteIds.has(item.id))}
                  >
                    <Heart
                      className={
                        favoriteIds.has(item.id)
                          ? "text-primary size-4 fill-current"
                          : "text-muted-foreground size-4"
                      }
                    />
                  </button>
                ) : null}
              </div>
            </div>
            <p className="text-muted-foreground mt-1 text-xs">
              {formatTime(item.starts_at)} – {formatTime(item.ends_at)}
            </p>
            <p className="text-muted-foreground mt-2 line-clamp-2 text-xs">
              {item.description ?? ""}
            </p>
            <Button type="button" size="sm" className="mt-3" onClick={() => onPlay(item)}>
              Watch live
            </Button>
          </article>
        ))}
      </div>
    </section>
  );
}

function Guide({
  programmes,
  stale,
  onPlay,
}: {
  readonly programmes: readonly LiveTVProgramme[];
  readonly stale: boolean;
  readonly onPlay: (item: LiveTVProgramme) => void;
}) {
  const channels = useMemo(
    () => Array.from(new Set(programmes.map((item) => item.channel_id))),
    [programmes],
  );
  return (
    <section className="space-y-3" data-testid="live-tv-guide">
      <div className="flex items-center justify-between">
        <h2 className="text-lg font-semibold">{liveTVT("program")}</h2>
        {stale ? <p className="text-warning text-xs">{liveTVT("stale")}</p> : null}
      </div>
      <div className="border-border overflow-x-auto rounded-xl border">
        <div className="bg-surface-raised text-muted-foreground grid min-w-[42rem] grid-cols-[8rem_1fr] border-b text-xs">
          <div className="p-3">{liveTVT("channels")}</div>
          <div className="grid grid-cols-4">
            <span className="border-border border-l p-3">{liveTVT("now")}</span>
            <span className="border-border border-l p-3">{liveTVT("hours3")}</span>
            <span className="border-border border-l p-3">{liveTVT("hours6")}</span>
            <span className="border-border border-l p-3">{liveTVT("hours9")}</span>
          </div>
        </div>
        {channels.map((channelId) => (
          <div
            key={channelId}
            className="grid min-w-[42rem] grid-cols-[8rem_1fr] border-b last:border-b-0"
          >
            <div className="text-muted-foreground truncate p-3 text-xs">{channelId}</div>
            <div className="grid grid-cols-4">
              {programmes
                .filter((item) => item.channel_id === channelId)
                .slice(0, 4)
                .map((item) => (
                  <div key={item.id} className="border-border min-h-16 border-l p-3 text-xs">
                    <p className="truncate font-medium">{item.title}</p>
                    <p className="text-muted-foreground mt-1">{formatTime(item.starts_at)}</p>
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      className="mt-1"
                      onClick={() => onPlay(item)}
                    >
                      Watch live
                    </Button>
                  </div>
                ))}
            </div>
          </div>
        ))}
      </div>
    </section>
  );
}

export function LiveTVLibraryPage({
  libraryId,
  libraryName,
}: {
  readonly libraryId: number;
  readonly libraryName: string;
}) {
  const [searchParams, setSearchParams] = useSearchParams();
  const { tab, view } = parseLiveTVSearchParams(searchParams);
  const [live, setLive] = useState<{
    readonly title: string;
    readonly channelId: string;
    readonly streamUrl: string;
    readonly grantId: string;
  } | null>(null);
  const now = new Date();
  const from = new Date(now.getTime() - 60 * 60 * 1000).toISOString();
  const to = new Date(now.getTime() + 23 * 60 * 60 * 1000).toISOString();
  const channelsQuery = useLiveTVChannels(libraryId);
  const guideQuery = useLiveTVGuide(libraryId, from, to);
  const favoriteChannelsQuery = useLiveTVFavorites(libraryId, "channels");
  const favoriteProgrammesQuery = useLiveTVFavorites(libraryId, "programmes");
  const toggle = useToggleLiveTVFavorite(libraryId, "channels");
  const toggleProgrammes = useToggleLiveTVFavorite(libraryId, "programmes");
  const { profile } = useCurrentProfile();
  const locale = profile?.language?.startsWith("de") ? "de" : "en";
  const channels = channelsQuery.data?.items ?? [];
  const favoriteIds = new Set((favoriteChannelsQuery.data ?? []).map((item) => item.id));
  const favoriteProgrammeIds = new Set((favoriteProgrammesQuery.data ?? []).map((item) => item.id));
  const updateState = (next: Partial<{ readonly tab: LiveTVTab; readonly view: LiveTVView }>) =>
    setSearchParams(
      updateLiveTVSearchParams(searchParams, { tab: next.tab ?? tab, view: next.view ?? view }),
      { replace: true },
    );
  const playChannel = async (channelId: string, title: string) => {
    const response = await resolveLiveTVPlayback(libraryId, channelId);
    if (response.playable && response.url && response.grant_id)
      setLive({ title, channelId, streamUrl: response.url, grantId: response.grant_id });
  };
  const playProgramme = (item: LiveTVProgramme) => void playChannel(item.channel_id, item.title);
  if (live) return <LiveTVPlayer {...live} onStop={() => setLive(null)} />;
  if (channelsQuery.isLoading)
    return (
      <div className="page-shell space-y-6 py-6" data-testid="live-tv-loading">
        <Skeleton className="h-10 w-56" />
        <div className="grid grid-cols-2 gap-4 sm:grid-cols-4 lg:grid-cols-6">
          {Array.from({ length: 12 }, (_, index) => (
            <Skeleton key={index} className="aspect-video rounded-xl" />
          ))}
        </div>
      </div>
    );
  if (channelsQuery.isError)
    return (
      <div
        className="page-shell flex min-h-[50vh] items-center justify-center"
        data-testid="live-tv-error"
      >
        <div className="surface-panel max-w-md rounded-xl p-6 text-center">
          <p className="font-semibold">{liveTVT("error")}</p>
          <Button className="mt-4" onClick={() => void channelsQuery.refetch()}>
            {liveTVT("retry")}
          </Button>
        </div>
      </div>
    );
  const guide = guideQuery.data?.items ?? [];
  const programmes =
    tab === "favorites" ? guide.filter((item) => favoriteProgrammeIds.has(item.id)) : guide;
  return (
    <div className="page-shell space-y-8 py-6" data-testid="live-tv-library">
      <header className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <p className="text-muted-foreground text-xs font-semibold tracking-[0.16em] uppercase">
            {liveTVT("liveTV", locale)}
          </p>
          <h1 className="mt-1 text-3xl font-bold tracking-tight">{libraryName}</h1>
        </div>
        <div className="flex items-center gap-2">
          <Button
            type="button"
            variant={view === "grid" ? "secondary" : "ghost"}
            size="icon"
            aria-label={liveTVT("grid", locale)}
            aria-pressed={view === "grid"}
            onClick={() => updateState({ view: "grid" })}
          >
            <LayoutGrid className="size-4" />
          </Button>
          <Button
            type="button"
            variant={view === "list" ? "secondary" : "ghost"}
            size="icon"
            aria-label={liveTVT("list", locale)}
            aria-pressed={view === "list"}
            onClick={() => updateState({ view: "list" })}
          >
            <List className="size-4" />
          </Button>
        </div>
      </header>
      <nav
        className="border-border flex gap-1 border-b"
        aria-label={liveTVT("liveTV", locale)}
        data-testid="live-tv-tabs"
      >
        {(["favorites", "program", "channels"] as const).map((candidate) => (
          <button
            key={candidate}
            type="button"
            className={`border-b-2 px-3 py-2 text-sm font-medium transition-colors ${tab === candidate ? "border-primary text-foreground" : "text-muted-foreground hover:text-foreground border-transparent"}`}
            aria-current={tab === candidate ? "page" : undefined}
            onClick={() => updateState({ tab: candidate })}
          >
            {liveTVT(candidate, locale)}
          </button>
        ))}
      </nav>
      {channels.length === 0 ? (
        <div
          className="border-border text-muted-foreground rounded-xl border border-dashed p-12 text-center"
          data-testid="live-tv-empty"
        >
          {liveTVT("empty", locale)}
        </div>
      ) : null}
      {guideQuery.data?.stale ? (
        <div
          className="border-warning/30 bg-warning/10 text-warning rounded-xl border p-3 text-sm"
          role="status"
        >
          {liveTVT("stale", locale)}
          {guideQuery.data.refresh_error ? ` ${guideQuery.data.refresh_error}` : ""}
        </div>
      ) : null}
      {tab === "program" ? (
        <Guide programmes={guide} stale={guideQuery.data?.stale ?? false} onPlay={playProgramme} />
      ) : null}
      {tab !== "program" ? (
        <div
          className={
            view === "grid" ? "grid grid-cols-2 gap-4 sm:grid-cols-4 lg:grid-cols-6" : "space-y-3"
          }
        >
          {channels
            .filter((channel) => tab !== "favorites" || favoriteIds.has(channel.id))
            .map((channel) => (
              <ChannelCard
                key={channel.id}
                channel={channel}
                favorite={favoriteIds.has(channel.id)}
                onToggle={() =>
                  toggle.mutate({ id: channel.id, favorite: !favoriteIds.has(channel.id) })
                }
                view={view}
                onPlay={() => void playChannel(channel.id, channel.name)}
              />
            ))}
        </div>
      ) : null}
      <ProgrammeRow
        title={liveTVT("now", locale)}
        items={programmes}
        favoriteIds={favoriteProgrammeIds}
        onToggle={(id, favorite) => toggleProgrammes.mutate({ id, favorite })}
        onPlay={playProgramme}
      />
    </div>
  );
}
