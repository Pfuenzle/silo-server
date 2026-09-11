import { Trash2 } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { liveTVT } from "@/lib/i18n";
import type { LiveTVSourceDraft } from "./liveTVSources";

export function LiveTVSourceDraftRow({
  source,
  index,
  errors,
  sourceKeyReadOnly = false,
  onUpdate,
  onRemove,
}: {
  readonly source: LiveTVSourceDraft;
  readonly index: number;
  readonly errors: Partial<Record<"source_key" | "name" | "location", string>>;
  readonly sourceKeyReadOnly?: boolean;
  readonly onUpdate: (index: number, value: Partial<LiveTVSourceDraft>) => void;
  readonly onRemove: (index: number) => void;
}) {
  return (
    <div
      className="border-border bg-surface grid gap-2 rounded-lg border p-3 sm:grid-cols-[9rem_1fr_1fr_2fr_auto] sm:items-end"
      data-testid={`live-tv-source-row-${index}`}
    >
      <div>
        <Label htmlFor={`live-tv-kind-${index}`}>{liveTVT("sourceStatus")}</Label>
        <Select
          value={source.kind}
          onValueChange={(value) => onUpdate(index, { kind: value === "epg" ? "epg" : "playlist" })}
        >
          <SelectTrigger id={`live-tv-kind-${index}`}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="playlist">{liveTVT("playlist")}</SelectItem>
            <SelectItem value="epg">{liveTVT("epg")}</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <SourceField
        id={`live-tv-key-${index}`}
        label={liveTVT("sourceKey")}
        value={source.source_key}
        error={errors.source_key}
        onChange={(value) => onUpdate(index, { source_key: value })}
        readOnly={sourceKeyReadOnly}
      />
      <SourceField
        id={`live-tv-name-${index}`}
        label={liveTVT("sourceName")}
        value={source.name}
        error={errors.name}
        onChange={(value) => onUpdate(index, { name: value })}
      />
      <SourceField
        id={`live-tv-location-${index}`}
        label={liveTVT("location")}
        value={source.location}
        error={errors.location}
        type="url"
        onChange={(value) => onUpdate(index, { location: value })}
      />
      <Button
        type="button"
        size="icon"
        variant="ghost"
        aria-label={liveTVT("remove")}
        onClick={() => onRemove(index)}
      >
        <Trash2 className="size-4" />
      </Button>
    </div>
  );
}

function SourceField({
  id,
  label,
  value,
  error,
  type = "text",
  readOnly = false,
  onChange,
}: {
  readonly id: string;
  readonly label: string;
  readonly value: string;
  readonly error?: string;
  readonly type?: "text" | "url";
  readonly onChange: (value: string) => void;
  readonly readOnly?: boolean;
}) {
  return (
    <div>
      <Label htmlFor={id}>{label}</Label>
      <Input
        id={id}
        type={type}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        readOnly={readOnly}
        aria-invalid={Boolean(error)}
        autoComplete={type === "url" ? "off" : undefined}
      />
      {error ? <p className="text-destructive text-xs">{error}</p> : null}
    </div>
  );
}
