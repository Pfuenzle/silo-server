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

interface WebKitFullscreenVideo extends HTMLVideoElement {
  webkitDisplayingFullscreen?: boolean;
  webkitEnterFullscreen?: () => void;
  webkitExitFullscreen?: () => void;
}

function supportsWebKitFullscreen(video: HTMLVideoElement): video is WebKitFullscreenVideo {
  return "webkitEnterFullscreen" in video;
}

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
  const surfaceRef = useRef<HTMLDivElement>(null);
  const retryPlayerRef = useRef<(() => void) | undefined>(undefined);
  const audioTrackSwitchRef = useRef<((index: number) => boolean) | undefined>(undefined);
  const audioTracksRef = useRef<PlayerAudioTrack[]>([]);
  const audioTrackIdsRef = useRef<string[]>([]);
  const revokedGrantRef = useRef<string | null>(null);
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
  const [audioSwitchSupported, setAudioSwitchSupported] = useState(false);
  const [livePaused, setLivePaused] = useState(false);
  const { profile } = useCurrentProfile();
  const locale = profile?.language?.startsWith("de") ? "de" : "en";
  const streamIsSafe = isSiloPlaybackUrl(streamUrl);

  const revokePlayback = useCallback(() => {
    if (revokedGrantRef.current === grantId) return;
    revokedGrantRef.current = grantId;
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
      setLivePaused(false);
    };
    const handlePause = () => {
      setPlaying(false);
      setLivePaused(true);
    };
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
          player.on(MPEGts.Events.ERROR, () => setState("error"));
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
            setAudioSwitchSupported(false);
            const player = new Hls({
              enableWorker: true,
              lowLatencyMode: true,
              backBufferLength: 0,
            });
            player.attachMedia(video);
            const updateAudioTracks = () => {
              const availableTracks = player.audioTracks;
              setAudioSwitchSupported(availableTracks.length > 0);
              if (availableTracks.length === 0) return;
              const activeTrack = availableTracks[player.audioTrack];
              const activeId = activeTrack?.groupId;
              const negotiatedIndex = audioTrackIdsRef.current.findIndex(
                (id) => id === activeTrack?.groupId,
              );
              if (negotiatedIndex >= 0) setActiveAudioIndex(negotiatedIndex);
              if (activeId) {
                audioTrackSwitchRef.current = (index) => {
                  const requested = audioTracksRef.current[index];
                  if (!requested) return false;
                  const engineIndex = availableTracks.findIndex(
                    (track) => track.groupId === audioTrackIdsRef.current[index],
                  );
                  if (engineIndex < 0) return false;
                  player.audioTrack = engineIndex;
                  return true;
                };
              }
            };
            player.on(Hls.Events.MANIFEST_PARSED, updateAudioTracks);
            player.on(Hls.Events.AUDIO_TRACKS_UPDATED, updateAudioTracks);
            audioTrackSwitchRef.current = (index) => {
              const requested = audioTracksRef.current[index];
              if (!requested) return false;
              const engineIndex = player.audioTracks.findIndex(
                (track) => track.groupId === audioTrackIdsRef.current[index],
              );
              if (engineIndex < 0) return false;
              player.audioTrack = engineIndex;
              return true;
            };
            player.on(Hls.Events.ERROR, () => setState("error"));
            destroyPlayer = () => player.destroy();
            retryPlayerRef.current = () => player.startLoad();
            player.loadSource(streamURL);
            void Promise.resolve(video.play()).catch(() => setState("error"));
          } else {
            setAudioSwitchSupported(false);
            setAudioTracks([]);
            audioTracksRef.current = [];
            audioTrackIdsRef.current = [];
            setActiveAudioIndex(0);
            audioTrackSwitchRef.current = undefined;
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
      audioTrackSwitchRef.current = undefined;
      retryPlayerRef.current = undefined;
      video.removeEventListener("playing", handlePlaying);
      video.removeEventListener("pause", handlePause);
      video.removeEventListener("error", handleError);
      video.pause();
      video.removeAttribute("src");
      video.load();
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
          const playerAudioTracks = negotiatedAudioTracks.map((track) => ({
            language: track.language,
            title: track.name,
            default: track.default,
          }));
          audioTrackIdsRef.current = negotiatedAudioTracks.map((track) => track.id);
          audioTracksRef.current = playerAudioTracks;
          setAudioTracks(playerAudioTracks);
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
    const handleWebKitFullscreenChange = (event: Event) => {
      const video = event.currentTarget;
      if (video instanceof HTMLVideoElement && supportsWebKitFullscreen(video)) {
        setIsFullscreen(Boolean(video.webkitDisplayingFullscreen));
      }
    };
    document.addEventListener("fullscreenchange", handleFullscreenChange);
    const video = videoRef.current;
    video?.addEventListener("webkitbeginfullscreen", handleWebKitFullscreenChange);
    video?.addEventListener("webkitendfullscreen", handleWebKitFullscreenChange);
    return () => {
      document.removeEventListener("fullscreenchange", handleFullscreenChange);
      video?.removeEventListener("webkitbeginfullscreen", handleWebKitFullscreenChange);
      video?.removeEventListener("webkitendfullscreen", handleWebKitFullscreenChange);
    };
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
      if (video.seekable.length > 0) {
        video.currentTime = video.seekable.end(video.seekable.length - 1);
      }
      void Promise.resolve(video.play()).catch((error: unknown) => {
        if (error instanceof Error) setState("error");
      });
      return;
    }
    video.pause();
  };

  const toggleFullscreen = () => {
    const surface = surfaceRef.current;
    const video = videoRef.current;
    if (!surface) return;
    if (document.fullscreenElement) {
      void document.exitFullscreen();
      setIsFullscreen(false);
      return;
    }
    if (video && supportsWebKitFullscreen(video) && video.webkitDisplayingFullscreen) {
      video.webkitExitFullscreen?.();
      setIsFullscreen(false);
      return;
    }
    if (typeof surface.requestFullscreen === "function") {
      void surface
        .requestFullscreen()
        .then(() => setIsFullscreen(true))
        .catch(() => {
          if (video && supportsWebKitFullscreen(video)) {
            video.webkitEnterFullscreen?.();
            setIsFullscreen(true);
          } else {
            setIsFullscreen(false);
          }
        });
      return;
    }
    if (video && supportsWebKitFullscreen(video)) {
      video.webkitEnterFullscreen?.();
      setIsFullscreen(true);
    }
  };

  return (
    <div
      ref={surfaceRef}
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
      {livePaused ? (
        <div className="pointer-events-none absolute inset-x-0 top-4 z-20 flex justify-center px-4">
          <p className="rounded-lg border border-amber-300/40 bg-black/75 px-4 py-2 text-center text-xs text-white/90 shadow-lg backdrop-blur">
            Live playback is paused. Resume starts at the provider&apos;s live edge; this player
            cannot go back behind the live window.
          </p>
        </div>
      ) : null}
      <p role="status" aria-label={liveTVT("playerPlaying", locale)} className="sr-only">
        {livePaused
          ? `${liveTVT("playerPlaying", locale)} (paused)`
          : !streamIsSafe || state === "error"
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
          onAudioSelect={
            audioTracks.length > 0 && audioSwitchSupported
              ? (index) => {
                  if (audioTrackSwitchRef.current?.(index)) setActiveAudioIndex(index);
                }
              : undefined
          }
          audioUnavailable={audioTracks.length === 0 || !audioSwitchSupported}
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
          playbackInfoAvailable={false}
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
