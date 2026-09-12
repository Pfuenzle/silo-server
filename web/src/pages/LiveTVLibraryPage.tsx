import { useMemo, useState } from "react";
import { Heart, LayoutGrid, List, Radio, Star } from "lucide-react";
import { useSearchParams } from "react-router";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Tabs } from "@/components/ui/tabs";
import LibraryHeader from "@/components/LibraryHeader";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "sonner";
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
import { liveTVArtwork } from "./liveTVArtwork";
import { liveTVPlaybackOutcome } from "./liveTVPlayback";

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

type LiveTVInfoItem = {
  readonly channelId: string;
  readonly channelName: string;
  readonly title: string;
  readonly artwork: unknown;
  readonly description?: string;
  readonly category?: string;
  readonly rating: unknown;
  readonly startsAt?: string;
  readonly endsAt?: string;
};

function programmeStatus(item: LiveTVInfoItem, locale: "en" | "de"): string | null {
  if (!item.startsAt || !item.endsAt) return null;
  return Date.parse(item.startsAt) <= Date.now() && Date.parse(item.endsAt) > Date.now()
    ? liveTVT("currentlyAiring", locale)
    : liveTVT("programmeTime", locale);
}

function LiveTVInfoDialog({
  item,
  locale,
  open,
  onOpenChange,
  onWatch,
  playbackFailure,
}: {
  readonly item: LiveTVInfoItem | null;
  readonly locale: "en" | "de";
  readonly open: boolean;
  readonly onOpenChange: (open: boolean) => void;
  readonly onWatch: () => void;
  readonly playbackFailure: string | null;
}) {
  const image = item ? liveTVArtwork(item.artwork) : null;
  const status = item ? programmeStatus(item, locale) : null;
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent data-testid="live-tv-info-dialog">
        {item ? (
          <>
            <DialogHeader>
              <DialogTitle>{item.title}</DialogTitle>
              <DialogDescription>{item.channelName}</DialogDescription>
            </DialogHeader>
            <div className="space-y-4">
              <div className="bg-muted flex aspect-video items-center justify-center overflow-hidden rounded-lg">
                {image ? (
                  <img src={image} alt="" className="size-full object-cover" width="640" height="360" />
                ) : (
                  <Radio className="text-muted-foreground size-10" aria-hidden="true" />
                )}
              </div>
              <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm">
                {status ? <span className="text-primary font-medium">{status}</span> : null}
                {item.startsAt && item.endsAt ? (
                  <span className="text-muted-foreground">
                    {formatTime(item.startsAt)} – {formatTime(item.endsAt)}
                  </span>
                ) : null}
                {item.category ? <span className="text-muted-foreground">{item.category}</span> : null}
                <Rating value={item.rating} />
              </div>
              {item.description ? <p className="text-muted-foreground text-sm">{item.description}</p> : null}
              {playbackFailure ? <p className="text-destructive text-sm" role="alert">{playbackFailure}</p> : null}
            </div>
            <DialogFooter>
              <Button type="button" onClick={onWatch}>{liveTVT("watchChannel", locale)}</Button>
            </DialogFooter>
          </>
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

function ChannelCard({
  channel,
  favorite,
  onToggle,
  view,
  onPlay,
  currentProgramme,
  onOpen,
  watchLabel,
}: {
  readonly channel: LiveTVChannel;
  readonly favorite: boolean;
  readonly onToggle: () => void;
  readonly view: LiveTVView;
  readonly onPlay: () => void;
  readonly currentProgramme?: LiveTVProgramme;
  readonly onOpen: () => void;
  readonly watchLabel: string;
}) {
  const image = liveTVArtwork(channel.artwork);
  return (
    <article
      className={
        view === "list"
          ? "border-border bg-surface flex items-center gap-3 rounded-xl border p-3"
          : "group border-border bg-surface overflow-hidden rounded-xl border"
      }
      data-testid={`live-tv-channel-${channel.id}`}
      role="button"
      tabIndex={0}
      onClick={onOpen}
      onKeyDown={(event) => {
        if (event.key === "Enter" || event.key === " ") {
          event.preventDefault();
          onOpen();
        }
      }}
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
              {currentProgramme?.title ?? channel.category ?? liveTVT("liveTV")}
            </p>
            {currentProgramme ? (
              <p className="text-primary truncate text-xs font-medium">
                {liveTVT("currentlyAiring", "en")}
              </p>
            ) : null}
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
          <Button
            type="button"
            size="sm"
            onClick={(event) => {
              event.stopPropagation();
              onPlay();
            }}
          >
            {watchLabel}
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
  onOpen,
  watchLabel,
}: {
  readonly title: string;
  readonly items: readonly LiveTVProgramme[];
  readonly favoriteIds?: ReadonlySet<string>;
  readonly onToggle?: (id: string, favorite: boolean) => void;
  readonly onPlay: (item: LiveTVProgramme) => void;
  readonly onOpen: (item: LiveTVProgramme) => void;
  readonly watchLabel: string;
}) {
  if (items.length === 0) return null;
  return (
    <section className="space-y-3" data-testid="live-tv-programme-row">
      <h2 className="text-lg font-semibold">{title}</h2>
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {items.map((item) => (
          <article
            key={item.id}
            className="border-border bg-surface rounded-xl border p-3"
            data-testid={`live-tv-programme-${item.id}`}
            role="button"
            tabIndex={0}
            onClick={() => onOpen(item)}
            onKeyDown={(event) => {
              if (event.key === "Enter" || event.key === " ") {
                event.preventDefault();
                onOpen(item);
              }
            }}
          >
            {liveTVArtwork(item.artwork) ? (
              <img
                src={liveTVArtwork(item.artwork) ?? undefined}
                alt=""
                className="mb-3 aspect-video w-full rounded-lg object-cover"
                width="320"
                height="180"
              />
            ) : null}
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
            <Button
              type="button"
              size="sm"
              className="mt-3"
              onClick={(event) => {
                event.stopPropagation();
                onPlay(item);
              }}
            >
              {watchLabel}
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
  onOpen,
  watchLabel,
  locale,
}: {
  readonly programmes: readonly LiveTVProgramme[];
  readonly stale: boolean;
  readonly onPlay: (item: LiveTVProgramme) => void;
  readonly onOpen: (item: LiveTVProgramme) => void;
  readonly watchLabel: string;
  readonly locale: "en" | "de";
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
      {programmes.length === 0 ? (
        <div className="text-muted-foreground rounded-xl border border-dashed p-12 text-center">
          {liveTVT("guideEmpty", locale)}
        </div>
      ) : null}
      {programmes.length > 0 ? (
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
                    <div
                      key={item.id}
                      className="border-border min-h-16 cursor-pointer border-l p-3 text-xs"
                      data-testid={`live-tv-programme-${item.id}`}
                      role="button"
                      tabIndex={0}
                      onClick={() => onOpen(item)}
                      onKeyDown={(event) => {
                        if (event.key === "Enter" || event.key === " ") {
                          event.preventDefault();
                          onOpen(item);
                        }
                      }}
                    >
                      <p className="truncate font-medium">{item.title}</p>
                      <p className="text-muted-foreground mt-1">{formatTime(item.starts_at)}</p>
                      <Button
                        type="button"
                        variant="ghost"
                        size="sm"
                        className="mt-1"
                        onClick={(event) => {
                          event.stopPropagation();
                          onPlay(item);
                        }}
                      >
                        {watchLabel}
                      </Button>
                    </div>
                  ))}
              </div>
            </div>
          ))}
        </div>
      ) : null}
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
  const [playbackFailure, setPlaybackFailure] = useState<{
    readonly channelId: string;
    readonly title: string;
    readonly message: string;
  } | null>(null);
  const [selectedItem, setSelectedItem] = useState<LiveTVInfoItem | null>(null);
  const [{ from, to, now }] = useState(() => {
    const now = Date.now();
    return {
      now,
      from: new Date(now - 60 * 60 * 1000).toISOString(),
      to: new Date(now + 23 * 60 * 60 * 1000).toISOString(),
    };
  });
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
      (current) => {
        const currentState = parseLiveTVSearchParams(current);
        return updateLiveTVSearchParams(current, {
          tab: next.tab ?? currentState.tab,
          view: next.view ?? currentState.view,
        });
      },
      { replace: true },
    );
  const playChannel = async (channelId: string, title: string) => {
    setPlaybackFailure(null);
    try {
      const outcome = liveTVPlaybackOutcome(await resolveLiveTVPlayback(libraryId, channelId));
      if (outcome.kind === "playable") {
        setLive({ title, channelId, streamUrl: outcome.url, grantId: outcome.grantId });
        return;
      }
      const message =
        outcome.kind === "unavailable"
          ? liveTVT("playbackUnavailable", locale)
          : liveTVT("playbackError", locale);
      setPlaybackFailure({ channelId, title, message });
      toast.error(message);
    } catch {
      const message = liveTVT("playbackError", locale);
      setPlaybackFailure({ channelId, title, message });
      toast.error(message);
    }
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
  const currentProgrammes = new Map(
    guide
      .filter((item) => Date.parse(item.starts_at) <= now && Date.parse(item.ends_at) > now)
      .map((item) => [item.channel_id, item]),
  );
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
      <Tabs
        value={tab}
        onValueChange={(value) => {
          if (value === "favorites" || value === "program" || value === "channels") {
            updateState({ tab: value });
          }
        }}
      >
        <LibraryHeader
          libraryName={libraryName}
          availableTabs={["favorites", "program", "channels"]}
          tabLabels={{
            favorites: liveTVT("favorites", locale),
            program: liveTVT("program", locale),
            channels: liveTVT("channels", locale),
          }}
        />
        <div data-testid="live-tv-tabs">
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
          {playbackFailure ? (
            <div className="border-destructive/30 bg-destructive/10 rounded-xl border p-4" role="alert">
              <p className="font-semibold">{playbackFailure.message}</p>
              <Button
                type="button"
                className="mt-3"
                onClick={() => void playChannel(playbackFailure.channelId, playbackFailure.title)}
              >
                {liveTVT("retry", locale)}
              </Button>
            </div>
          ) : null}
          {tab === "program" && guideQuery.isLoading ? (
            <div
              className="text-muted-foreground rounded-xl border border-dashed p-12 text-center"
              data-testid="live-tv-guide-loading"
            >
              {liveTVT("guideLoading", locale)}
            </div>
          ) : null}
          {tab === "program" && guideQuery.isError ? (
            <div
              className="border-border rounded-xl border border-dashed p-12 text-center"
              data-testid="live-tv-guide-error"
            >
              <p className="font-semibold">{liveTVT("guideError", locale)}</p>
              <Button className="mt-4" onClick={() => void guideQuery.refetch()}>
                {liveTVT("guideRetry", locale)}
              </Button>
            </div>
          ) : null}
          {tab === "program" && !guideQuery.isLoading && !guideQuery.isError ? (
            <Guide
              programmes={guide}
              stale={guideQuery.data?.stale ?? false}
              onPlay={playProgramme}
              onOpen={(item) =>
                setSelectedItem({
                  channelId: item.channel_id,
                  channelName: item.channel_name ?? item.channel_id,
                  title: item.title,
                  artwork: item.artwork,
                  description: item.description,
                  rating: item.rating,
                  startsAt: item.starts_at,
                  endsAt: item.ends_at,
                })
              }
              watchLabel={liveTVT("watchLive", locale)}
              locale={locale}
            />
          ) : null}
          {tab !== "program" ? (
            <div
              className={
                view === "grid"
                  ? "grid grid-cols-2 gap-4 sm:grid-cols-4 lg:grid-cols-6"
                  : "space-y-3"
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
                    currentProgramme={currentProgrammes.get(channel.id)}
                    onOpen={() => {
                      const programme = currentProgrammes.get(channel.id);
                      setSelectedItem({
                        channelId: channel.id,
                        channelName: channel.name,
                        title: programme?.title ?? channel.name,
                        artwork: programme?.artwork ?? channel.artwork,
                        description: programme?.description,
                        category: channel.category,
                        rating: programme?.rating ?? channel.rating,
                        startsAt: programme?.starts_at,
                        endsAt: programme?.ends_at,
                      });
                    }}
                    onPlay={() => void playChannel(channel.id, channel.name)}
                    watchLabel={liveTVT("watchLive", locale)}
                  />
                ))}
            </div>
          ) : null}
          {tab !== "channels" ? (
            <ProgrammeRow
              title={liveTVT("now", locale)}
              items={programmes}
              favoriteIds={favoriteProgrammeIds}
              onToggle={(id, favorite) => toggleProgrammes.mutate({ id, favorite })}
              onPlay={playProgramme}
              onOpen={(item) =>
                setSelectedItem({
                  channelId: item.channel_id,
                  channelName: item.channel_name ?? item.channel_id,
                  title: item.title,
                  artwork: item.artwork,
                  description: item.description,
                  rating: item.rating,
                  startsAt: item.starts_at,
                  endsAt: item.ends_at,
                })
              }
              watchLabel={liveTVT("watchLive", locale)}
            />
          ) : null}
        </div>
      </Tabs>
      <LiveTVInfoDialog
        item={selectedItem}
        locale={locale}
        open={selectedItem !== null}
        onOpenChange={(open) => {
          if (!open) setSelectedItem(null);
        }}
        onWatch={() => {
          if (selectedItem) void playChannel(selectedItem.channelId, selectedItem.title);
        }}
        playbackFailure={
          playbackFailure && playbackFailure.channelId === selectedItem?.channelId
            ? playbackFailure.message
            : null
        }
      />
    </div>
  );
}
