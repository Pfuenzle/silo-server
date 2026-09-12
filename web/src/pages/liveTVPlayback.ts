type LiveTVPlaybackResponseLike = {
  readonly playable: boolean;
  readonly url?: string;
  readonly grant_id?: string;
  readonly error_code?: string;
};

export type LiveTVPlaybackOutcome =
  | { readonly kind: "playable"; readonly url: string; readonly grantId: string }
  | { readonly kind: "unavailable" }
  | { readonly kind: "error"; readonly error: string };

export function liveTVPlaybackOutcome(
  response: LiveTVPlaybackResponseLike | Error,
): LiveTVPlaybackOutcome {
  if (response instanceof Error) return { kind: "error", error: response.message };
  if (response.playable && response.url && response.grant_id && isSiloPlaybackUrl(response.url)) {
    return { kind: "playable", url: response.url, grantId: response.grant_id };
  }
  return { kind: "unavailable" };
}
import { isSiloPlaybackUrl } from "@/api/livetv";
