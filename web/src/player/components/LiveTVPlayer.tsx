import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "@/api/client";
import { isSiloPlaybackUrl } from "@/api/livetv";
import { liveTVT } from "@/lib/i18n";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";

interface LiveTVPlayerProps {
  readonly channelId: string;
  readonly title: string;
  readonly streamUrl: string;
  readonly grantId: string;
  readonly mode?: "direct" | "hls";
  readonly startupTimeoutMs?: number;
  readonly onStop?: () => void;
}

const DEFAULT_STARTUP_TIMEOUT_MS = 15_000;

export function LiveTVPlayer({
  channelId,
  title,
  streamUrl,
  grantId,
  mode = "hls",
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
    let destroyPlayer: (() => void) | undefined;
    let retryPlayer: (() => void) | undefined;
    let cancelled = false;
    const streamURL = streamUrl;
    if (mode === "direct") {
      void import("mpegts.js")
        .then(({ default: MPEGts }) => {
          if (cancelled) return;
          const player = MPEGts.createPlayer({ type: "mpegts", url: streamURL, isLive: true });
          destroyPlayer = () => player.destroy();
          player.attachMediaElement(video);
          player.load();
          retryPlayer = () => {
            player.unload();
            player.load();
            void player.play().catch(() => setState("error"));
          };
          void player.play().catch(() => setState("error"));
        })
        .catch((error: unknown) => {
          if (error instanceof Error) setState("error");
        });
    } else {
      void import("hls.js")
        .then(({ default: Hls }) => {
          if (cancelled) return;
          if (Hls.isSupported()) {
            const player = new Hls({
              enableWorker: true,
              lowLatencyMode: true,
              backBufferLength: 0,
            });
            player.attachMedia(video);
            player.loadSource(streamURL);
            destroyPlayer = () => player.destroy();
            retryPlayer = () => player.startLoad();
          } else {
            video.src = streamURL;
          }
        })
        .catch((error: unknown) => {
          if (error instanceof Error) setState("error");
        });
    }
    video.addEventListener("playing", handlePlaying);
    video.addEventListener("error", handleError);
    const timeout = window.setTimeout(() => {
      setState((current) => (current === "starting" ? "error" : current));
    }, startupTimeoutMs);

    return () => {
      cancelled = true;
      window.clearTimeout(timeout);
      destroyPlayer?.();
      video.pause();
      video.removeAttribute("src");
      video.load();
      video.removeEventListener("playing", handlePlaying);
      video.removeEventListener("error", handleError);
    };
  }, [mode, startupTimeoutMs, streamIsSafe, streamUrl]);

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
    retryPlayer?.();
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
