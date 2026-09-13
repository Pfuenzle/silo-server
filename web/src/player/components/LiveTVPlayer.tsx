import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "@/api/client";
import {
  isSiloPlaybackUrl,
  liveTVQualityResponseSchema,
  type LiveTVQualityResponse,
} from "@/api/livetv";
import { liveTVT } from "@/lib/i18n";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import { PlayerControls } from "./PlayerControls";

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
  const retryPlayerRef = useRef<(() => void) | undefined>(undefined);
  const stoppedRef = useRef(false);
  const [state, setState] = useState<"starting" | "playing" | "error" | "reconnecting">("starting");
  const [playing, setPlaying] = useState(false);
  const [controlsVisible, setControlsVisible] = useState(true);
  const [volume, setVolume] = useState(1);
  const [muted, setMuted] = useState(false);
  const [isFullscreen, setIsFullscreen] = useState(false);
  const [quality, setQuality] = useState<LiveTVQualityResponse>({
    options: [],
    transcoding_supported: false,
    unsupported_reason: "loading",
  });
  const [qualityError, setQualityError] = useState<string | null>(null);
  const [qualityRevision, setQualityRevision] = useState(0);
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

    const handlePlaying = () => {
      setState("playing");
      setPlaying(true);
    };
    const handlePause = () => setPlaying(false);
    const handleError = () => setState("error");
    let destroyPlayer: (() => void) | undefined;
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
          retryPlayerRef.current = () => {
            player.unload();
            player.load();
            void Promise.resolve(player.play()).catch(() => setState("error"));
          };
          void Promise.resolve(player.play()).catch(() => setState("error"));
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
            retryPlayerRef.current = () => player.startLoad();
          } else {
            video.src = streamURL;
          }
        })
        .catch((error: unknown) => {
          if (error instanceof Error) setState("error");
        });
    }
    video.addEventListener("playing", handlePlaying);
    video.addEventListener("pause", handlePause);
    video.addEventListener("error", handleError);
    const timeout = window.setTimeout(() => {
      setState((current) => (current === "starting" ? "error" : current));
    }, startupTimeoutMs);

    return () => {
      cancelled = true;
      window.clearTimeout(timeout);
      destroyPlayer?.();
      retryPlayerRef.current = undefined;
      video.pause();
      video.removeAttribute("src");
      video.load();
      video.removeEventListener("playing", handlePlaying);
      video.removeEventListener("pause", handlePause);
      video.removeEventListener("error", handleError);
    };
  }, [mode, qualityRevision, startupTimeoutMs, streamIsSafe, streamUrl]);

  useEffect(() => {
    let cancelled = false;
    void Promise.resolve(api<unknown>(`/livetv/playback/${encodeURIComponent(grantId)}/qualities`))
      .then((value) => {
        if (!cancelled) setQuality(liveTVQualityResponseSchema.parse(value));
      })
      .catch((error: unknown) => {
        if (!cancelled)
          setQualityError(
            error instanceof Error ? error.message : "Live TV quality is unavailable",
          );
      });
    return () => {
      cancelled = true;
    };
  }, [grantId]);

  const selectQuality = (id: string) => {
    setQualityError(null);
    void Promise.resolve(
      api<unknown>(`/livetv/playback/${encodeURIComponent(grantId)}/quality`, {
        method: "POST",
        body: JSON.stringify({ id }),
        headers: { "Content-Type": "application/json" },
      }),
    )
      .then((value) => {
        const selected = liveTVQualityResponseSchema.parse(value);
        setQuality((current) => ({ ...current, active_id: selected.active_id }));
        setQualityRevision((revision) => revision + 1);
      })
      .catch((error: unknown) => {
        setQualityError(error instanceof Error ? error.message : "Live TV quality change failed");
      });
  };

  useEffect(() => {
    return () => {
      revokePlayback();
    };
  }, [revokePlayback]);

  useEffect(() => {
    const handleFullscreenChange = () => setIsFullscreen(document.fullscreenElement !== null);
    document.addEventListener("fullscreenchange", handleFullscreenChange);
    return () => document.removeEventListener("fullscreenchange", handleFullscreenChange);
  }, []);

  const stop = () => {
    videoRef.current?.pause();
    revokePlayback();
    onStop?.();
  };

  const retry = () => {
    const video = videoRef.current;
    if (!video) return;
    setState("reconnecting");
    retryPlayerRef.current?.();
    void Promise.resolve(video.play()).catch((error: unknown) => {
      if (error instanceof Error) setState("error");
    });
  };

  const togglePlayback = () => {
    const video = videoRef.current;
    if (!video) return;
    if (video.paused) {
      void Promise.resolve(video.play()).catch((error: unknown) => {
        if (error instanceof Error) setState("error");
      });
      return;
    }
    video.pause();
  };

  const toggleFullscreen = () => {
    const video = videoRef.current;
    if (!video) return;
    if (document.fullscreenElement) {
      void document.exitFullscreen();
      setIsFullscreen(false);
      return;
    }
    void video
      .requestFullscreen()
      .then(() => setIsFullscreen(true))
      .catch(() => setIsFullscreen(false));
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
        muted={muted}
        onVolumeChange={(event) => {
          setVolume(event.currentTarget.volume);
          setMuted(event.currentTarget.muted);
        }}
        onClick={() => setControlsVisible((visible) => !visible)}
      />
      <p role="status" aria-label={liveTVT("playerPlaying", locale)} className="sr-only">
        {!streamIsSafe || state === "error"
          ? liveTVT("playerError", locale)
          : state === "playing"
            ? liveTVT("playerPlaying", locale)
            : state === "reconnecting"
              ? liveTVT("playerReconnecting", locale)
              : liveTVT("playerStarting", locale)}
      </p>
      {controlsVisible ? (
        <PlayerControls
          live
          liveLabel={liveTVT("playerPlaying", locale)}
          visible
          playing={playing}
          currentTime={0}
          duration={0}
          buffered={null}
          volume={volume}
          muted={muted}
          isFullscreen={isFullscreen}
          subtitleTracks={[]}
          activeSubtitleIndex={null}
          onSubtitleSelect={() => {}}
          subtitleDelayMs={0}
          onSubtitleDelayChange={() => {}}
          audioTracks={[]}
          activeAudioIndex={0}
          qualityOptions={
            mode === "hls"
              ? quality.options.map((option) => ({
                  id: option.id,
                  label: option.label,
                  sublabel: option.bitrate_kbps ? `${option.bitrate_kbps} kbps` : "Live",
                  resolution: option.height ? `${option.height}p` : "Live",
                  bitrateKbps: option.bitrate_kbps ?? 0,
                  isOriginal: false,
                }))
              : []
          }
          activeQualityId={quality.active_id ?? "auto"}
          isTranscoding={false}
          qualityError={
            qualityError ??
            (quality.unsupported_reason === "server_transcoding_unavailable"
              ? "Server transcoding unavailable for this live source"
              : null)
          }
          onQualitySelect={selectQuality}
          showPlaybackInfo={false}
          onTogglePlaybackInfo={() => {}}
          onPlayPause={togglePlayback}
          onSeek={() => {}}
          onVolumeChange={(nextVolume) => {
            const video = videoRef.current;
            if (!video) return;
            video.volume = nextVolume;
            setVolume(nextVolume);
          }}
          onMutedChange={(nextMuted) => {
            const video = videoRef.current;
            if (!video) return;
            video.muted = nextMuted;
            setMuted(nextMuted);
          }}
          onFullscreenToggle={toggleFullscreen}
          onStop={stop}
          stopLabel={liveTVT("playerStop", locale)}
          onSurfaceTap={() => setControlsVisible((visible) => !visible)}
          title={title}
        />
      ) : null}
      {state === "error" || !streamIsSafe ? (
        <div className="absolute inset-0 z-40 flex items-center justify-center bg-black/70">
          <button type="button" onClick={retry}>
            {liveTVT("playerRetry", locale)}
          </button>
        </div>
      ) : null}
    </div>
  );
}
