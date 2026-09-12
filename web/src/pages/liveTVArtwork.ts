type ArtworkValue = {
  readonly logo?: unknown;
  readonly path?: unknown;
  readonly url?: unknown;
};

function isArtworkValue(value: unknown): value is ArtworkValue {
  return typeof value === "object" && value !== null;
}

function isRenderableImageURL(value: unknown): value is string {
  if (typeof value !== "string" || value.trim() === "") return false;
  const candidate = value.trim();
  if (candidate.startsWith("/api/")) return true;
  try {
    const parsed = new URL(candidate);
    return (
      parsed.protocol === "https:" &&
      (parsed.searchParams.has("X-Amz-Signature") ||
        parsed.searchParams.has("X-Goog-Signature") ||
        parsed.searchParams.has("verify"))
    );
  } catch {
    return false;
  }
}

export function liveTVArtwork(value: unknown): string | null {
  if (!isArtworkValue(value)) return null;
  const candidates = [value.url, value.logo, value.path];
  return candidates.find(isRenderableImageURL) ?? null;
}
