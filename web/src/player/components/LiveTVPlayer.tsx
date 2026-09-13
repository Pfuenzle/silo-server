import { useCallback, useEffect, useRef, useState } from "react";
import { api, getAccessToken } from "@/api/client";
import { isSiloPlaybackUrl } from "@/api/livetv";
import { liveTVT } from "@/lib/i18n";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import { storage } from "@/utils/storage";

interface LiveTVPlayerProps {
  readonly channelId: string;
  readonly title: string;
  readonly streamUrl: string;
  readonly grantId: string;
  readonly startupTimeoutMs?: number;
  readonly onStop?: () => void;
}

const DEFAULT_STARTUP_TIMEOUT_MS = 15_000;

export function LiveTVPlayer({
  channelId,
  title,
  streamUrl,
  grantId,
  startupTimeoutMs = DEFAULT_STARTUP_TIMEOUT_MS,
  onStop,
}: LiveTVPlayerProps) {
  const videoRef = useRef<HTMLVideoElement>(null);
  const stoppedRef = useRef(false);
  const [state, setState] = useState<"starting" | "playing" | "error" | "reconnecting">("starting");
  const { profile } = useCurrentProfile();
  const locale = profile?.language?.startsWith("de") ? "de" : "en";
  const streamIsSafe = isSiloPlaybackUrl(streamUrl);

  const revokePlayback = useCallback(() => {
    if (stoppedRef.current) return;
    stoppedRef.current = true;
    void Promise.resolve(
      api(`/livetv/playback/${encodeURIComponent(grantId)}`, {
        method: "DELETE",
        keepalive: true,
      }),
    ).catch((error: unknown) => {
      if (error instanceof Error) console.warn("Live playback cleanup failed", error.message);
    });
  }, [grantId]);

  useEffect(() => {
    if (!streamIsSafe) return;

    const video = videoRef.current;
    if (!video) return;

    const handlePlaying = () => setState("playing");
    const handleError = () => setState("reconnecting");
    let hls: {
      destroy: () => void;
      loadSource: (url: string) => void;
      attachMedia: (element: HTMLVideoElement) => void;
    } | null = null;
    const accessToken = getAccessToken();
    const profileId = storage.get(storage.KEYS.PROFILE_ID);
    const streamParams = new URLSearchParams();
    if (accessToken) streamParams.set("token", accessToken);
    if (profileId) streamParams.set("profile_id", profileId);
    const query = streamParams.toString();
    const streamURL = query
      ? `${streamUrl}${streamUrl.includes("?") ? "&" : "?"}${query}`
      : streamUrl;
    void import("hls.js")
      .then(({ default: Hls }) => {
        if (Hls.isSupported()) {
          hls = new Hls({
            enableWorker: true,
            lowLatencyMode: true,
            backBufferLength: 0,
            xhrSetup: (request: XMLHttpRequest) => {
              if (accessToken) request.setRequestHeader("Authorization", `Bearer ${accessToken}`);
              if (profileId) request.setRequestHeader("X-Profile-Id", profileId);
            },
          });
          hls.loadSource(streamURL);
          hls.attachMedia(video);
        } else {
          video.src = streamURL;
        }
      })
      .catch((error: unknown) => {
        if (error instanceof Error) setState("error");
      });
    video.addEventListener("playing", handlePlaying);
    video.addEventListener("error", handleError);
    const timeout = window.setTimeout(() => {
      setState((current) => (current === "starting" ? "error" : current));
    }, startupTimeoutMs);

    return () => {
      window.clearTimeout(timeout);
      hls?.destroy();
      video.removeEventListener("playing", handlePlaying);
      video.removeEventListener("error", handleError);
    };
  }, [startupTimeoutMs, streamIsSafe, streamUrl]);

  useEffect(() => {
    return () => {
      revokePlayback();
    };
  }, [revokePlayback]);

  const stop = () => {
    videoRef.current?.pause();
    revokePlayback();
    onStop?.();
  };

  const retry = () => {
    const video = videoRef.current;
    if (!video) return;
    setState("reconnecting");
    video.load();
    void video.play().catch((error: unknown) => {
      if (error instanceof Error) setState("error");
    });
  };

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black"
      data-channel-id={channelId}
    >
      <video
        ref={videoRef}
        className="size-full object-contain"
        autoPlay
        playsInline
        controls={false}
      />
      <div className="absolute inset-x-0 bottom-0 flex items-center justify-between gap-4 bg-black/70 p-4 text-white">
        <div className="min-w-0">
          <p className="truncate text-sm font-semibold">{title}</p>
          <p
            role="status"
            aria-label={liveTVT("playerPlaying", locale)}
            className="text-primary text-xs font-semibold tracking-wide uppercase"
          >
            {!streamIsSafe || state === "error"
              ? liveTVT("playerError", locale)
              : state === "playing"
                ? liveTVT("playerPlaying", locale)
                : state === "reconnecting"
                  ? liveTVT("playerReconnecting", locale)
                  : liveTVT("playerStarting", locale)}
          </p>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          {!streamIsSafe || state === "error" ? (
            <button type="button" onClick={retry}>
              {liveTVT("playerRetry", locale)}
            </button>
          ) : null}
          <button type="button" onClick={stop} aria-label={liveTVT("playerStop", locale)}>
            {liveTVT("playerStop", locale)}
          </button>
        </div>
      </div>
    </div>
  );
}
