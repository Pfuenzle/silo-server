import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import {
  liveTVChannelSchema,
  liveTVGuideSchema,
  liveTVProgrammeSchema,
  liveTVSourceSchema,
  type LiveTVChannel,
  type LiveTVGuide,
  type LiveTVPage,
  type LiveTVProgramme,
  type LiveTVSource,
  liveTVPlaybackResponseSchema,
  liveTVHomeSectionsSchema,
  type LiveTVPlaybackResponse,
} from "@/api/livetv";
import { liveTVKeys } from "./keys";

const parsePage =
  <T>(schema: { parse: (value: unknown) => T }) =>
  (value: unknown): LiveTVPage<T> => {
    const response = value as { items?: unknown; total?: number; limit?: number; offset?: number };
    const items = Array.isArray(response.items)
      ? response.items.map((item) => schema.parse(item))
      : [];
    return {
      items,
      total: response.total ?? items.length,
      limit: response.limit ?? items.length,
      offset: response.offset ?? 0,
    };
  };

export function useLiveTVSources(libraryId: number, enabled = true) {
  return useQuery({
    queryKey: liveTVKeys.sources(libraryId),
    queryFn: () =>
      api<{ items: readonly unknown[] }>(`/livetv/libraries/${libraryId}/sources/`).then(
        (data) => ({ items: data.items.map((item) => liveTVSourceSchema.parse(item)) }),
      ),
    enabled,
  });
}

export function useLiveTVChannels(libraryId: number, enabled = true) {
  return useQuery({
    queryKey: liveTVKeys.channels(libraryId),
    queryFn: () =>
      api<unknown>(`/livetv/libraries/${libraryId}/channels?limit=200`).then(
        parsePage(liveTVChannelSchema),
      ),
    enabled,
  });
}

export function useLiveTVGuide(libraryId: number, from: string, to: string, enabled = true) {
  return useQuery({
    queryKey: liveTVKeys.guide(libraryId, from, to),
    queryFn: () =>
      api<unknown>(
        `/livetv/libraries/${libraryId}/guide?from=${encodeURIComponent(from)}&to=${encodeURIComponent(to)}`,
      ).then((value) => liveTVGuideSchema.parse(value)),
    enabled,
  });
}

export function useLiveTVHomeSections(libraryId: number, enabled = true) {
  return useQuery({
    queryKey: liveTVKeys.home(libraryId),
    queryFn: () =>
      api<unknown>(`/livetv/libraries/${libraryId}/home-sections`).then((value) =>
        liveTVHomeSectionsSchema.parse(value),
      ),
    enabled,
  });
}

export function resolveLiveTVPlayback(
  libraryId: number,
  channelId: string,
): Promise<LiveTVPlaybackResponse> {
  return api<unknown>(
    `/livetv/libraries/${libraryId}/channels/${encodeURIComponent(channelId)}/playback`,
  ).then((value) => liveTVPlaybackResponseSchema.parse(value));
}

export function useLiveTVFavorites(libraryId: number, kind: "channels" | "programmes") {
  return useQuery({
    queryKey: liveTVKeys.favorites(libraryId, kind),
    queryFn: () =>
      api<{ channels?: readonly unknown[]; programmes?: readonly unknown[] }>(
        `/livetv/libraries/${libraryId}/favorites/${kind}`,
      ).then((data) =>
        (kind === "channels" ? (data.channels ?? []) : (data.programmes ?? [])).map((item) =>
          kind === "channels" ? liveTVChannelSchema.parse(item) : liveTVProgrammeSchema.parse(item),
        ),
      ),
  });
}

export function useToggleLiveTVFavorite(libraryId: number, kind: "channels" | "programmes") {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, favorite }: { readonly id: string; readonly favorite: boolean }) =>
      api<void>(`/livetv/libraries/${libraryId}/favorites/${kind}/${encodeURIComponent(id)}`, {
        method: favorite ? "PUT" : "DELETE",
      }),
    onMutate: async ({ id, favorite }) => {
      const key = liveTVKeys.favorites(libraryId, kind);
      await queryClient.cancelQueries({ queryKey: key });
      const previous = queryClient.getQueryData<readonly (LiveTVChannel | LiveTVProgramme)[]>(key);
      const candidate =
        kind === "channels"
          ? queryClient
              .getQueryData<LiveTVPage<LiveTVChannel>>(liveTVKeys.channels(libraryId))
              ?.items.find((item) => item.id === id)
          : queryClient
              .getQueriesData<LiveTVGuide>({
                queryKey: ["livetv", "guide", libraryId],
              })
              .flatMap(([, guide]) => guide?.items ?? [])
              .find((item) => item.id === id);
      queryClient.setQueryData(
        key,
        (current: readonly (LiveTVChannel | LiveTVProgramme)[] | undefined) => {
          const items = current ?? [];
          if (!favorite) return items.filter((item) => item.id !== id);
          if (items.some((item) => item.id === id) || candidate === undefined) return items;
          return [...items, candidate];
        },
      );
      return { previous };
    },
    onError: (_error, _variables, context) => {
      if (context?.previous !== undefined)
        queryClient.setQueryData(liveTVKeys.favorites(libraryId, kind), context.previous);
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: liveTVKeys.all });
    },
  });
}

export type { LiveTVChannel, LiveTVGuide, LiveTVProgramme, LiveTVSource };
