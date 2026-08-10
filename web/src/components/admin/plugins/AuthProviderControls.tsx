import { AlertTriangle } from "lucide-react";
import { useId, useState } from "react";

import type { PluginAuthBinding, PluginCapability, PluginInstallation } from "@/api/types";
import type { PluginAuthorizationMode } from "@/api/pluginAuthMappings";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { useSavePluginAuthBinding } from "@/hooks/queries/admin/plugins";

import { AuthGroupMappingsPanel } from "./AuthGroupMappingsPanel";

type Props = {
  readonly installation: PluginInstallation;
  readonly capability: PluginCapability;
  readonly binding: PluginAuthBinding | undefined;
  readonly displayOrder: number;
};

function authorizationMode(value: string): PluginAuthorizationMode {
  return value === "external_groups_v1" ? "external_groups_v1" : "none";
}

export function AuthProviderControls({ installation, capability, binding, displayOrder }: Props) {
  const id = useId();
  const saveBinding = useSavePluginAuthBinding();
  const [pendingMode, setPendingMode] = useState<PluginAuthorizationMode | null>(null);
  const enabled = binding?.enabled ?? false;
  const mode = binding?.authorization_mode ?? "none";

  function save(next: { readonly enabled: boolean; readonly mode: PluginAuthorizationMode }) {
    saveBinding.mutate({
      id: installation.id,
      body: {
        capability_id: capability.id,
        enabled: next.enabled,
        display_order: binding?.display_order ?? displayOrder,
        auto_provision: binding?.auto_provision ?? true,
        default_login: binding?.default_login ?? false,
        authorization_mode: next.mode,
      },
    });
  }

  return (
    <div className="space-y-3 rounded-lg border p-3">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <p className="text-sm font-medium">{capability.display_name || capability.id}</p>
          <p className="text-muted-foreground font-mono text-xs">{capability.id}</p>
        </div>
        <div className="flex items-center gap-2">
          <Label htmlFor={`${id}-enabled`}>Enabled</Label>
          <Switch
            id={`${id}-enabled`}
            checked={enabled}
            disabled={saveBinding.isPending}
            onCheckedChange={(checked) => save({ enabled: checked, mode })}
          />
        </div>
      </div>

      <div className="space-y-1.5">
        <Label htmlFor={`${id}-mode`}>Authorization mode</Label>
        <Select
          value={mode}
          disabled={!enabled || saveBinding.isPending}
          onValueChange={(value) => {
            const next = authorizationMode(value);
            if (next !== mode) setPendingMode(next);
          }}
        >
          <SelectTrigger id={`${id}-mode`} className="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="none">Local authorization</SelectItem>
            <SelectItem value="external_groups_v1">Authoritative external groups</SelectItem>
          </SelectContent>
        </Select>
        <p className="text-muted-foreground text-xs">
          Authoritative mode overwrites local role and access-group edits after every successful
          external login.
        </p>
      </div>

      {saveBinding.isError ? (
        <p role="alert" aria-live="assertive" className="text-destructive text-sm">
          {saveBinding.error instanceof Error
            ? saveBinding.error.message
            : "Failed to save auth binding"}
        </p>
      ) : null}

      <AuthGroupMappingsPanel
        key={`${enabled}-${mode}`}
        installationId={installation.id}
        capabilityId={capability.id}
        enabled={installation.enabled && enabled && mode === "external_groups_v1"}
      />

      <AlertDialog
        open={pendingMode !== null}
        onOpenChange={(open) => !open && setPendingMode(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle className="flex items-center gap-2">
              <AlertTriangle className="text-destructive size-5" />
              Change external authorization mode?
            </AlertDialogTitle>
            <AlertDialogDescription>
              This explicit opt-in changes the authority for roles and access groups. Saving demotes
              affected managed accounts to safe defaults and revokes existing sessions. Restart the
              server to apply the binding change.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                if (pendingMode !== null) save({ enabled, mode: pendingMode });
                setPendingMode(null);
              }}
            >
              Confirm authorization change
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
