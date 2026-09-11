export type LiveTVSourceKind = "playlist" | "epg";

export type LiveTVSourceDraft = {
  readonly kind: LiveTVSourceKind;
  readonly source_key: string;
  readonly name: string;
  readonly location: string;
  readonly enabled: boolean;
};

export type LiveTVSourceErrors = Partial<
  Record<"kind" | "source_key" | "name" | "location", string>
>;

export type LiveTVSourceErrorMap = Record<number, LiveTVSourceErrors>;

import { liveTVT } from "@/lib/i18n";

export function validateLiveTVSources(
  sources: readonly LiveTVSourceDraft[],
  locale = "en",
  allowEmptyLocationIndices: ReadonlySet<number> = new Set(),
): LiveTVSourceErrorMap {
  const errors: LiveTVSourceErrorMap = {};
  const seenKeys = new Set<string>();
  sources.forEach((source, index) => {
    const row: LiveTVSourceErrors = {};
    const key = source.source_key.trim();
    if (!source.name.trim()) row.name = liveTVT("sourceNameRequired", locale);
    if (!key) row.source_key = liveTVT("sourceKeyRequired", locale);
    if (seenKeys.has(key) && key) row.source_key = liveTVT("sourceKeyUnique", locale);
    seenKeys.add(key);
    const location = source.location.trim();
    if (location || !allowEmptyLocationIndices.has(index)) {
      try {
        const url = new URL(location);
        if (url.protocol !== "http:" && url.protocol !== "https:") {
          row.location = liveTVT("sourceURLInvalid", locale);
        }
      } catch {
        row.location = liveTVT("sourceURLInvalid", locale);
      }
    }
    if (Object.keys(row).length > 0) errors[index] = row;
  });
  return errors;
}
