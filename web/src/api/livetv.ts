import { z } from "zod";

export const liveTVSourceSchema = z.object({
  id: z.number(),
  library_id: z.number(),
  kind: z.string(),
  source_key: z.string(),
  name: z.string(),
  enabled: z.boolean(),
  refresh_state: z.string(),
  refresh_error: z.string().optional(),
  last_refresh_at: z.string().optional(),
});
export type LiveTVSource = z.infer<typeof liveTVSourceSchema>;
export const liveTVChannelSchema = z.object({
  id: z.string(),
  name: z.string(),
  number: z.string().optional(),
  category: z.string().optional(),
  artwork: z.unknown().optional(),
  rating: z.unknown().optional(),
  stale: z.boolean().optional(),
});
export type LiveTVChannel = z.infer<typeof liveTVChannelSchema>;
export const liveTVProgrammeSchema = z.object({
  id: z.string(),
  channel_id: z.string(),
  title: z.string(),
  description: z.string().optional(),
  starts_at: z.string(),
  ends_at: z.string(),
  artwork: z.unknown().optional(),
  rating: z.unknown().optional(),
});
export type LiveTVProgramme = z.infer<typeof liveTVProgrammeSchema>;
export const liveTVGuideSchema = z.object({
  library_id: z.number(),
  from: z.string(),
  to: z.string(),
  stale: z.boolean(),
  refresh_error: z.string().optional(),
  items: z.array(liveTVProgrammeSchema),
});
export type LiveTVGuide = z.infer<typeof liveTVGuideSchema>;
export type LiveTVPage<T> = {
  readonly items: readonly T[];
  readonly total: number;
  readonly limit: number;
  readonly offset: number;
};
export type LiveTVHomeSections = {
  readonly currently_airing: readonly LiveTVProgramme[];
  readonly favorite_channels_currently_airing: readonly LiveTVChannel[];
  readonly favorite_programmes_currently_airing: readonly LiveTVProgramme[];
  readonly top_rated_favorite_programmes: readonly LiveTVProgramme[];
  readonly upcoming_favorite_programmes: readonly LiveTVProgramme[];
};
export const liveTVHomeSectionsSchema = z.object({
  currently_airing: z.array(liveTVProgrammeSchema),
  favorite_channels_currently_airing: z.array(liveTVChannelSchema),
  favorite_programmes_currently_airing: z.array(liveTVProgrammeSchema),
  top_rated_favorite_programmes: z.array(liveTVProgrammeSchema),
  upcoming_favorite_programmes: z.array(liveTVProgrammeSchema),
});

export type LiveTVLocale = "en" | "de";

export const liveTVPlaybackResponseSchema = z.object({
  channel_id: z.string(),
  live: z.literal(true),
  playable: z.boolean(),
  url: z.string().optional(),
  grant_id: z.string().optional(),
  error_code: z.string().optional(),
});
export type LiveTVPlaybackResponse = z.infer<typeof liveTVPlaybackResponseSchema>;

export function isSiloPlaybackUrl(value: string, apiBaseUrl = "/api/v1"): boolean {
  try {
    const origin = typeof window === "undefined" ? "http://localhost" : window.location.origin;
    const url = new URL(value, origin);
    const base = new URL(apiBaseUrl, origin);
    return url.origin === base.origin && url.pathname.startsWith(`${base.pathname}/stream/live/`);
  } catch {
    return false;
  }
}
