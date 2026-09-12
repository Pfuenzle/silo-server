import { Radio, Star } from "lucide-react";
import type { UseQueryResult } from "@tanstack/react-query";
import { Link } from "react-router";

import type { LiveTVChannel, LiveTVHomeSections, LiveTVProgramme } from "@/api/livetv";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useLiveTVHomeSections } from "@/hooks/queries/livetv";
import { formatTime } from "@/lib/datetime";
import { liveTVT, type LiveTVLocale } from "@/lib/i18n";
import MediaCarousel from "@/components/MediaCarousel";

type LiveTVHomeQuery = Pick<
  UseQueryResult<LiveTVHomeSections>,
  "data" | "isLoading" | "isFetching" | "isError" | "refetch"
>;

type LiveTVHomeSectionProps = {
  readonly libraryId?: number;
  readonly locale?: LiveTVLocale;
  readonly query?: LiveTVHomeQuery;
  readonly currentOnly?: boolean;
};

export function LiveTVHomeSection({
  libraryId,
  locale = "en",
  query: providedQuery,
  currentOnly = false,
}: LiveTVHomeSectionProps) {
  const liveQuery = useLiveTVHomeSections(libraryId ?? 0, libraryId !== undefined);
  const query = providedQuery ?? liveQuery;

  if (query.isLoading && query.data === undefined) {
    return (
      <section className="space-y-3" aria-label={liveTVT("liveTV", locale)}>
        <Skeleton className="h-6 w-48" />
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
          {Array.from({ length: 4 }, (_, index) => (
            <Skeleton key={index} className="h-24 rounded-xl" />
          ))}
        </div>
        <span className="sr-only">{liveTVT("loading", locale)}</span>
      </section>
    );
  }

  if (query.isError && query.data === undefined) {
    return (
      <section className="surface-panel rounded-xl p-5" role="alert">
        <p className="font-semibold">{liveTVT("error", locale)}</p>
        <Button className="mt-3" size="sm" onClick={() => void query.refetch()}>
          {liveTVT("retry", locale)}
        </Button>
      </section>
    );
  }

  const sections = query.data;
  if (!sections || allEmpty(sections)) {
    return <p className="text-muted-foreground text-sm">{liveTVT("homeEmpty", locale)}</p>;
  }

  return (
    <section className="space-y-6" aria-label={liveTVT("liveTV", locale)}>
      {query.isFetching || query.isError ? (
        <p className="text-warning text-xs" role="status">
          {liveTVT("homeStale", locale)}
        </p>
      ) : null}
      <CurrentlyAiringRail
        title={liveTVT("homeCurrent", locale)}
        programmes={sections.currently_airing}
        libraryId={libraryId}
      />
      {currentOnly ? null : (
        <>
          <ChannelRail
            title={liveTVT("homeFavoriteChannels", locale)}
            channels={sections.favorite_channels_currently_airing}
          />
          <HomeRail
            title={liveTVT("homeFavoriteProgrammes", locale)}
            programmes={sections.favorite_programmes_currently_airing}
          />
          <HomeRail
            title={liveTVT("homeTopRated", locale)}
            programmes={sections.top_rated_favorite_programmes}
          />
          <HomeRail
            title={liveTVT("homeUpcoming", locale)}
            programmes={sections.upcoming_favorite_programmes}
          />
        </>
      )}
    </section>
  );
}

function CurrentlyAiringRail({
  title,
  programmes,
  libraryId,
}: {
  readonly title: string;
  readonly programmes: readonly LiveTVProgramme[];
  readonly libraryId?: number;
}) {
  if (programmes.length === 0) return null;

  return (
    <div data-testid="live-tv-currently-airing-row">
      <MediaCarousel title={title}>
        {programmes.map((programme) => (
          <article
            key={programme.id}
            className="border-border bg-surface flex w-[260px] shrink-0 flex-col gap-3 rounded-xl border p-4 sm:w-[315px]"
          >
            <div className="min-w-0">
              <h3 className="truncate text-sm font-semibold">{programme.title}</h3>
              <p className="text-muted-foreground mt-1 truncate text-xs">
                {programme.channel_name ?? programme.channel_id}
              </p>
              <p className="text-muted-foreground mt-2 text-xs">
                {formatTime(programme.starts_at)} – {formatTime(programme.ends_at)}
              </p>
            </div>
            {libraryId !== undefined ? (
              <Link
                to={`/library/${libraryId}?tab=program`}
                aria-label={`${liveTVT("watchLive")}: ${programme.title}`}
                className="text-primary focus-visible:ring-ring mt-auto inline-flex min-h-11 items-center text-xs font-semibold uppercase focus-visible:ring-2 focus-visible:outline-none"
              >
                {liveTVT("watchLive")}
              </Link>
            ) : null}
          </article>
        ))}
      </MediaCarousel>
    </div>
  );
}

function allEmpty(sections: LiveTVHomeSections): boolean {
  return Object.values(sections).every((items) => items.length === 0);
}

function HomeRail({
  title,
  programmes,
}: {
  readonly title: string;
  readonly programmes: readonly LiveTVProgramme[];
}) {
  if (programmes.length === 0) return null;
  return (
    <section className="space-y-3">
      <h2 className="text-lg font-semibold">{title}</h2>
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        {programmes.map((programme) => (
          <article key={programme.id} className="border-border bg-surface rounded-xl border p-3">
            <h3 className="truncate text-sm font-medium">{programme.title}</h3>
            <p className="text-muted-foreground mt-1 text-xs">
              {formatTime(programme.starts_at)} – {formatTime(programme.ends_at)}
            </p>
            {typeof programme.rating === "number" ? (
              <p className="text-muted-foreground mt-2 inline-flex items-center gap-1 text-xs">
                <Star className="size-3 fill-current" />
                {programme.rating.toFixed(1)}
              </p>
            ) : null}
          </article>
        ))}
      </div>
    </section>
  );
}

function ChannelRail({
  title,
  channels,
}: {
  readonly title: string;
  readonly channels: readonly LiveTVChannel[];
}) {
  if (channels.length === 0) return null;
  return (
    <section className="space-y-3">
      <h2 className="text-lg font-semibold">{title}</h2>
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        {channels.map((channel) => (
          <article
            key={channel.id}
            className="border-border bg-surface flex items-center gap-3 rounded-xl border p-3"
          >
            <Radio className="text-primary size-5 shrink-0" />
            <div className="min-w-0">
              <h3 className="truncate text-sm font-medium">{channel.name}</h3>
              <p className="text-muted-foreground text-xs">{channel.number ?? ""}</p>
            </div>
          </article>
        ))}
      </div>
    </section>
  );
}
