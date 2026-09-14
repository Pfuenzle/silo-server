import { useContext, useEffect, useMemo } from "react";
import { useLocation, useNavigate } from "react-router";
import { WatchPlaybackControllerContext } from "@/playback/watchPlaybackContext";
import { isLiveTVPlaybackStartInput } from "./watchRouteHelpers";

export default function LiveTVWatchRoute() {
  const controller = useContext(WatchPlaybackControllerContext);
  const location = useLocation();
  const navigate = useNavigate();
  const request = useMemo(() => {
    const state = location.state;
    if (!state || typeof state !== "object") return null;
    const livePlayback = "livePlayback" in state ? state.livePlayback : undefined;
    return isLiveTVPlaybackStartInput(livePlayback) ? livePlayback : null;
  }, [location.state]);

  useEffect(() => {
    if (request) {
      controller?.syncRouteRequest(request);
      return;
    }

    navigate("/", { replace: true });
  }, [controller, navigate, request]);

  useEffect(() => {
    if (!request || !controller) return;
    return () => controller.handleRouteExit(request.requestKey);
  }, [controller, request]);

  return null;
}
