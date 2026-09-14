import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "@/api/client";
import {
  isSiloPlaybackUrl,
  liveTVQualityResponseSchema,
  type LiveTVQualityResponse,
} from "@/api/livetv";
import { liveTVT } from "@/lib/i18n";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import type { PlayerAudioTrack } from "../types";
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
  const [audioTracks, setAudioTracks] = useState<PlayerAudioTrack[]>([]);
  const [activeAudioIndex, setActiveAudioIndex] = useState(0);
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
          const player = MPEGts.createPlayer({
            type: "mpegts",
            url: streamURL,
            isLive: true,
          });
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
            player.on(Hls.Events.ERROR, () => setState("error"));
            destroyPlayer = () => player.destroy();
            retryPlayerRef.current = () => player.startLoad();
            void Promise.resolve(video.play()).catch(() => setState("error"));
          } else {
            video.src = streamURL;
            void Promise.resolve(video.play()).catch(() => setState("error"));
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
        if (!cancelled) {
          const parsed = liveTVQualityResponseSchema.parse(value);
          setQuality(parsed);
          const negotiatedAudioTracks = parsed.audio_tracks ?? [];
          setAudioTracks(
            negotiatedAudioTracks.map((track) => ({
              language: track.language,
              title: track.name,
              default: track.default,
            })),
          );
          const defaultAudioIndex = negotiatedAudioTracks.findIndex((track) => track.default);
          setActiveAudioIndex(defaultAudioIndex >= 0 ? defaultAudioIndex : 0);
        }
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
      className="player-container absolute inset-0 flex items-center justify-center bg-black"
      data-channel-id={channelId}
      data-testid="player-surface"
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
          audioTracks={audioTracks}
          activeAudioIndex={activeAudioIndex}
          onAudioSelect={(index) => setActiveAudioIndex(index)}
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
          <div className="surface-panel-subtle flex max-w-sm flex-col items-center gap-3 rounded-xl px-6 py-5 text-center">
            <p data-testid="live-player-error" role="alert" className="text-sm text-white">
              {liveTVT("playerError", locale)}
            </p>
            <p className="text-xs text-white/60">{liveTVT("playerRetryExplanation", locale)}</p>
            {streamIsSafe ? (
              <button type="button" onClick={retry}>
                {liveTVT("playerRetry", locale)}
              </button>
            ) : null}
          </div>
        </div>
      ) : null}
    </div>
  );
}
