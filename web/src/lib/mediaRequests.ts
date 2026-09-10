import type {
  CreateMediaRequestInput,
  MediaRequest,
  MediaRequestOutcome,
  MediaRequestStatus,
  RequestMediaResult,
  RequestMediaType,
} from "@/api/types";
import { formatDate } from "@/lib/datetime";

export const REQUEST_STATUSES: Array<MediaRequestStatus | "all"> = [
  "all",
  "pending",
  "approved",
  "queued",
  "downloading",
  "completed",
];

export const REQUEST_OUTCOMES: Array<MediaRequestOutcome | "all"> = [
  "all",
  "active",
  "declined",
  "cancelled",
  "failed",
];

type BadgeVariant = "default" | "secondary" | "destructive" | "outline";

export function formatMediaType(mediaType: RequestMediaType): string {
  if (mediaType === "series") return "Series";
  if (mediaType === "audiobook") return "Audiobook";
  return "Movie";
}

export function formatRequestStatus(status?: MediaRequestStatus): string {
  switch (status) {
    case "pending":
      return "Pending";
    case "approved":
      return "Approved";
    case "queued":
      return "Queued";
    case "downloading":
      return "Downloading";
    case "completed":
      return "Completed";
    default:
      return "Requested";
  }
}

export function requestStatusBadgeVariant(status?: MediaRequestStatus): BadgeVariant {
  switch (status) {
    case "completed":
      return "default";
    case "pending":
      return "outline";
    default:
      return "secondary";
  }
}

export function formatRequestOutcome(outcome?: MediaRequestOutcome): string {
  switch (outcome) {
    case "active":
      return "Active";
    case "declined":
      return "Declined";
    case "cancelled":
      return "Cancelled";
    case "failed":
      return "Failed";
    default:
      return "Active";
  }
}

export function requestOutcomeBadgeVariant(outcome?: MediaRequestOutcome): BadgeVariant {
  switch (outcome) {
    case "failed":
    case "declined":
    case "cancelled":
      return "destructive";
    case "active":
      return "secondary";
    default:
      return "outline";
  }
}

export function formatRequestReason(reason?: string): string {
  switch (reason) {
    case "already_requested":
      return "Already requested";
    case "already_available":
      return "Available";
    case "requests_disabled":
      return "Requests disabled";
    case "blocked":
      return "Blocked";
    case "quota_exceeded":
      return "Request limit reached";
    default:
      return "Unavailable";
  }
}

export function tmdbImageURL(path?: string, size = "w342"): string | null {
  if (!path) return null;
  return `https://image.tmdb.org/t/p/${size}${path}`;
}

export function requestInputFromMediaResult(item: RequestMediaResult): CreateMediaRequestInput {
  return {
    media_type: item.media_type,
    ...(item.tmdb_id > 0 ? { tmdb_id: item.tmdb_id } : {}),
    ...(item.provider ? { provider: item.provider } : {}),
    ...(item.provider_item_id ? { provider_item_id: item.provider_item_id } : {}),
    title: item.title,
    year: item.year || undefined,
    overview: item.overview || undefined,
    poster_path: item.poster_path || undefined,
    backdrop_path: item.backdrop_path || undefined,
  };
}

export function formatRequestDate(request: Pick<MediaRequest, "created_at">): string {
  return formatDate(request.created_at, "medium");
}

export type AudiobookRequestDisplay = {
  label: string;
  detail: string | null;
  href: string | null;
  isCompleted: boolean;
  isFailed: boolean;
};

export function audiobookRequestDisplay(request: MediaRequest): AudiobookRequestDisplay {
  const href = siloAudiobookHref(request.silo_audiobook_link);
  const externalStatus = request.external_status?.trim().toLowerCase();
  const detail = request.external_detail?.trim() || request.last_error?.trim() || null;

  if (request.outcome === "declined" || request.outcome === "cancelled") {
    return {
      label: formatRequestOutcome(request.outcome),
      detail,
      href: null,
      isCompleted: false,
      isFailed: true,
    };
  }
  if (request.outcome === "failed" || externalStatus === "failed") {
    return {
      label: "Failed",
      detail: detail ?? "Listenarr reported a failure. Retry the request or contact an administrator.",
      href: null,
      isCompleted: false,
      isFailed: true,
    };
  }
  if (externalStatus === "completed" && !href) {
    return {
      label: "Failed",
      detail: detail ?? "Silo library link is not available yet. Refresh or retry the request.",
      href: null,
      isCompleted: false,
      isFailed: true,
    };
  }
  switch (externalStatus) {
    case "queued":
      return { label: "Queued", detail, href: null, isCompleted: false, isFailed: false };
    case "downloading":
      return { label: "Downloading", detail, href: null, isCompleted: false, isFailed: false };
    case "imported":
      return { label: "Imported", detail, href: null, isCompleted: false, isFailed: false };
    case "scanning":
      return { label: "Scanning", detail, href: null, isCompleted: false, isFailed: false };
    case "completed":
      return { label: "Completed", detail, href, isCompleted: true, isFailed: false };
    default:
      return {
        label: formatRequestStatus(request.status),
        detail,
        href: null,
        isCompleted: false,
        isFailed: false,
      };
  }
}

function siloAudiobookHref(link: string | null | undefined): string | null {
  const prefix = "/api/v1/items/";
  if (!link?.startsWith(prefix)) return null;
  const contentID = link.slice(prefix.length);
  if (!contentID || contentID.includes("/") || contentID.includes("?") || contentID.includes("#")) {
    return null;
  }
  return `/item/${encodeURIComponent(contentID)}`;
}
