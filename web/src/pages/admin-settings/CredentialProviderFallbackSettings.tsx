import { ArrowDown, ArrowUp } from "lucide-react";
import { useMemo, useState } from "react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { useAuth } from "@/hooks/useAuth";
import {
  useCredentialProviderFallback,
  useUpdateCredentialProviderFallback,
} from "@/hooks/queries/admin/settings";

interface ProviderRow {
  readonly id: string;
  readonly name: string;
  readonly description: string;
}

const EMPTY_PROVIDER_IDS: readonly string[] = [];

function move<T>(items: readonly T[], index: number, direction: -1 | 1): T[] {
  const target = index + direction;
  if (index < 0 || target < 0 || target >= items.length) return [...items];
  const next = [...items];
  const current = next[index];
  const replacement = next[target];
  if (current === undefined || replacement === undefined) return next;
  next[index] = replacement;
  next[target] = current;
  return next;
}

export default function CredentialProviderFallbackSettings() {
  const { providers } = useAuth();
  const policyQuery = useCredentialProviderFallback();
  const savePolicy = useUpdateCredentialProviderFallback();
  const credentialProviders = useMemo(
    () => providers.filter((provider) => provider.mode === "credentials"),
    [providers],
  );
  const providerMap = useMemo(
    () => new Map(credentialProviders.map((provider) => [provider.id, provider])),
    [credentialProviders],
  );
  const serverOrder = policyQuery.data?.provider_ids ?? EMPTY_PROVIDER_IDS;
  const completeServerOrder = useMemo(
    () => [
      ...serverOrder,
      ...credentialProviders
        .map((provider) => provider.id)
        .filter((id) => !serverOrder.includes(id)),
    ],
    [credentialProviders, serverOrder],
  );
  const [draftOrder, setDraftOrder] = useState<string[] | null | undefined>(undefined);
  const order = draftOrder ?? completeServerOrder;
  const automatic = draftOrder === null || (draftOrder === undefined && serverOrder.length === 0);

  const rows: ProviderRow[] = order.flatMap((id) => {
    const provider = providerMap.get(id);
    if (!provider) return [];
    return [
      {
        id,
        name: provider.display_name,
        description:
          id === "local"
            ? "Built-in local account credentials."
            : "External credentials tried according to this order.",
      },
    ];
  });
  const invalidOrder =
    new Set(order).size !== order.length || rows.length !== credentialProviders.length;
  const dirty = automatic
    ? serverOrder.length > 0
    : order.join("\u0000") !== serverOrder.join("\u0000");

  async function save() {
    if (invalidOrder) {
      toast.error("The credential provider order is invalid.");
      return;
    }
    try {
      await savePolicy.mutateAsync({ provider_ids: automatic ? [] : order });
      toast.success(
        automatic ? "Automatic provider order restored" : "Credential provider order saved",
      );
    } catch {
      toast.error("Unable to save credential provider order.");
    }
  }

  if (policyQuery.isLoading) {
    return (
      <div role="status" className="text-muted-foreground text-sm">
        Loading credential provider order...
      </div>
    );
  }

  if (policyQuery.isError) {
    return (
      <p role="alert" className="text-destructive text-sm">
        Unable to load credential provider order.
      </p>
    );
  }

  return (
    <section
      aria-labelledby="credential-provider-fallback-heading"
      className="surface-panel rounded-2xl p-4 sm:p-5"
    >
      <div className="space-y-2">
        <h3 id="credential-provider-fallback-heading" className="text-base font-semibold">
          Credential provider fallback order
        </h3>
        <p className="text-muted-foreground text-sm leading-relaxed">
          Password sign-in uses the server policy automatically. Choose an order here only when you
          want to override that policy. OAuth providers remain a separate sign-in method.
        </p>
        <p className="text-muted-foreground text-xs leading-relaxed">
          Local account means Silo&apos;s built-in credentials. Other entries represent external
          credential providers such as LDAP.
        </p>
      </div>
      <ol className="mt-4 space-y-2" aria-label="Credential provider order">
        {rows.map((row, index) => (
          <li
            key={row.id}
            className="border-border/60 bg-surface flex items-center gap-3 rounded-lg border p-3"
          >
            <span className="text-muted-foreground w-6 text-center text-sm" aria-hidden="true">
              {index + 1}
            </span>
            <div className="min-w-0 flex-1">
              <p className="truncate text-sm font-medium">{row.name}</p>
              <p className="text-muted-foreground text-xs">{row.description}</p>
            </div>
            <div className="flex gap-1">
              <Button
                type="button"
                variant="outline"
                size="icon-sm"
                aria-label={`Move ${row.name} up`}
                disabled={index === 0}
                onClick={() => {
                  setDraftOrder((current) => move(current ?? completeServerOrder, index, -1));
                }}
              >
                <ArrowUp aria-hidden="true" />
              </Button>
              <Button
                type="button"
                variant="outline"
                size="icon-sm"
                aria-label={`Move ${row.name} down`}
                disabled={index === rows.length - 1}
                onClick={() => {
                  setDraftOrder((current) => move(current ?? completeServerOrder, index, 1));
                }}
              >
                <ArrowDown aria-hidden="true" />
              </Button>
            </div>
          </li>
        ))}
      </ol>
      <div className="mt-4 flex flex-wrap items-center gap-2">
        <Button
          type="button"
          onClick={() => void save()}
          disabled={!dirty || savePolicy.isPending || invalidOrder}
        >
          {savePolicy.isPending ? "Saving..." : "Save provider order"}
        </Button>
        <Button
          type="button"
          variant="outline"
          onClick={() => {
            setDraftOrder(null);
          }}
          disabled={automatic && serverOrder.length === 0}
        >
          Use automatic server policy
        </Button>
        <span className="text-muted-foreground text-xs" role="status" aria-live="polite">
          {automatic ? "Automatic server policy" : "Explicit provider order"}
        </span>
      </div>
    </section>
  );
}
