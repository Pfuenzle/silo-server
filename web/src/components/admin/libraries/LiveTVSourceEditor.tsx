import { useState } from "react";
import { Pencil, Plus, RefreshCw, Trash2 } from "lucide-react";
import { api } from "@/api/client";
import { Button } from "@/components/ui/button";
import { useLiveTVSources } from "@/hooks/queries/livetv";
import { liveTVT } from "@/lib/i18n";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import { validateLiveTVSources, type LiveTVSourceDraft } from "./liveTVSources";
import type { LiveTVSource } from "@/api/livetv";
import { LiveTVSourceDraftRow } from "./LiveTVSourceDraftRow";

const emptySource = (): LiveTVSourceDraft => ({
  kind: "playlist",
  source_key: "",
  name: "",
  location: "",
  enabled: true,
});

const draftFromSource = (source: LiveTVSource): LiveTVSourceDraft => ({
  kind: source.kind === "epg" ? "epg" : "playlist",
  source_key: source.source_key,
  name: source.name,
  location: "",
  enabled: source.enabled,
});

export function LiveTVSourceEditor({ libraryId }: { readonly libraryId: number }) {
  const query = useLiveTVSources(libraryId);
  const { profile } = useCurrentProfile();
  const locale = profile?.language?.startsWith("de") ? "de" : "en";
  const t = (key: Parameters<typeof liveTVT>[0]) => liveTVT(key, locale);
  const [drafts, setDrafts] = useState<LiveTVSourceDraft[]>([]);
  const [editingKeys, setEditingKeys] = useState<Record<number, string>>({});
  const [submitted, setSubmitted] = useState(false);
  const [mutationError, setMutationError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const editableLocationIndices = new Set(Object.keys(editingKeys).map(Number));
  const errors = submitted ? validateLiveTVSources(drafts, locale, editableLocationIndices) : {};
  const update = (index: number, value: Partial<LiveTVSourceDraft>) => {
    setDrafts((current) =>
      current.map((source, currentIndex) =>
        currentIndex === index ? { ...source, ...value } : source,
      ),
    );
  };
  const startEdit = (source: LiveTVSource) => {
    setSaved(false);
    setMutationError(null);
    setEditingKeys((current) => ({ ...current, [drafts.length]: source.source_key }));
    setDrafts((current) => [...current, draftFromSource(source)]);
  };
  const save = async () => {
    setSubmitted(true);
    setMutationError(null);
    setSaved(false);
    if (Object.keys(validateLiveTVSources(drafts, locale, editableLocationIndices)).length > 0)
      return;
    try {
      await Promise.all(
        drafts.map((source, index) => {
          const originalKey = editingKeys[index];
          const editing = originalKey !== undefined;
          const path = editing
            ? `/livetv/libraries/${libraryId}/sources/${encodeURIComponent(originalKey)}`
            : `/livetv/libraries/${libraryId}/sources/`;
          return api(path, { method: editing ? "PUT" : "POST", body: JSON.stringify(source) });
        }),
      );
      setDrafts([]);
      setEditingKeys({});
      setSaved(true);
      await query.refetch();
    } catch (error) {
      setMutationError(error instanceof Error ? error.message : liveTVT("saveError"));
    }
  };

  return (
    <section
      className="border-border min-h-0 shrink-0 border-t px-5 py-4 sm:px-6"
      data-testid="live-tv-source-editor"
    >
      <div className="mb-3 flex items-center justify-between gap-3">
        <div>
          <h3 className="text-sm font-semibold">{liveTVT("sourcesTitle")}</h3>
          <p className="text-muted-foreground text-xs">{liveTVT("sourceHint")}</p>
        </div>
        <Button
          type="button"
          size="sm"
          variant="outline"
          onClick={() => setDrafts((current) => [...current, emptySource()])}
        >
          <Plus className="mr-1 size-3.5" />
          {liveTVT("addSource")}
        </Button>
      </div>
      <div className="max-h-56 space-y-3 overflow-y-auto" aria-live="polite">
        {(query.data?.items ?? []).map((source) => (
          <div
            key={source.source_key}
            className="border-border bg-surface flex flex-wrap items-center justify-between gap-3 rounded-lg border p-3"
            data-testid={`live-tv-configured-source-${source.source_key}`}
          >
            <div className="min-w-0">
              <p className="truncate text-sm font-medium">{source.name}</p>
              <p className="text-muted-foreground text-xs">
                {source.kind === "epg" ? liveTVT("epg") : liveTVT("playlist")} ·{" "}
                {source.refresh_state}
              </p>
              {source.refresh_error ? (
                <p className="text-warning text-xs">{source.refresh_error}</p>
              ) : null}
            </div>
            <div className="flex items-center gap-2">
              <span className="text-muted-foreground text-xs">
                {source.last_refresh_at
                  ? new Date(source.last_refresh_at).toLocaleString()
                  : liveTVT("configured")}
              </span>
              <Button
                type="button"
                size="sm"
                variant="outline"
                aria-label={`${liveTVT("edit")} ${source.name}`}
                onClick={() => startEdit(source)}
              >
                <Pencil className="mr-1 size-3.5" />
                {liveTVT("edit")}
              </Button>
              <Button
                type="button"
                size="sm"
                variant="outline"
                aria-label={`${liveTVT("refresh")} ${source.name}`}
                onClick={() =>
                  void api(
                    `/livetv/libraries/${libraryId}/sources/${encodeURIComponent(source.source_key)}/refresh`,
                    { method: "POST" },
                  )
                    .then(() => query.refetch())
                    .catch((error: unknown) => {
                      setMutationError(error instanceof Error ? error.message : t("refreshError"));
                    })
                }
              >
                <RefreshCw className="mr-1 size-3.5" />
                {liveTVT("refresh")}
              </Button>
              <Button
                type="button"
                size="icon"
                variant="ghost"
                aria-label={`${liveTVT("remove")} ${source.name}`}
                onClick={() =>
                  void api(
                    `/livetv/libraries/${libraryId}/sources/${encodeURIComponent(source.source_key)}`,
                    { method: "DELETE" },
                  )
                    .then(() => query.refetch())
                    .catch((error: unknown) => {
                      setMutationError(error instanceof Error ? error.message : t("removeError"));
                    })
                }
              >
                <Trash2 className="size-4" />
              </Button>
            </div>
          </div>
        ))}
        {drafts.map((source, index) => (
          <LiveTVSourceDraftRow
            key={`draft-${index}`}
            source={source}
            index={index}
            errors={errors[index] ?? {}}
            sourceKeyReadOnly={editingKeys[index] !== undefined}
            onUpdate={update}
            onRemove={(rowIndex) => {
              setDrafts((current) =>
                current.filter((_, currentIndex) => currentIndex !== rowIndex),
              );
              setEditingKeys((current) => {
                const next: Record<number, string> = {};
                Object.entries(current).forEach(([key, value]) => {
                  const currentIndex = Number(key);
                  if (currentIndex < rowIndex) next[currentIndex] = value;
                  if (currentIndex > rowIndex) next[currentIndex - 1] = value;
                });
                return next;
              });
            }}
          />
        ))}
      </div>
      {mutationError ? (
        <p className="text-destructive mt-3 text-xs" role="alert">
          {mutationError}
        </p>
      ) : null}
      {saved ? (
        <p className="text-success mt-3 text-xs" role="status">
          {liveTVT("saved")}
        </p>
      ) : null}
      <div className="mt-3 flex justify-end gap-2">
        <Button
          type="button"
          variant="outline"
          onClick={() => void query.refetch()}
          disabled={query.isFetching}
        >
          <RefreshCw className="mr-1 size-3.5" />
          {liveTVT("refresh")}
        </Button>
        <Button type="button" onClick={() => void save()}>
          {liveTVT("save")}
        </Button>
      </div>
    </section>
  );
}
