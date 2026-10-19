import { useCallback, useState } from "react";
import type { RequestMediaResult } from "@/api/types";
import { useCreateMediaRequest } from "@/hooks/queries/useRequests";
import { requestInputFromMediaResult, requestMediaKey } from "@/lib/mediaRequests";

/**
 * Requests metadata results from a list of cards and tracks which cards have a
 * request in flight. Each card waits on its own call: a second `mutate()`
 * would detach the first call's callbacks, leaving that card on "Sending".
 * The shared mutation still shows the success and failure toasts.
 */
export function useSubmitMediaRequest() {
  const { mutateAsync } = useCreateMediaRequest();
  const [pendingKeys, setPendingKeys] = useState<ReadonlySet<string>>(() => new Set());

  const submit = useCallback(
    (item: RequestMediaResult) => {
      const key = requestMediaKey(item);
      setPendingKeys((prev) => new Set(prev).add(key));
      mutateAsync(requestInputFromMediaResult(item))
        // The mutation's own onError already reported the failure.
        .catch(() => {})
        .finally(() => {
          setPendingKeys((prev) => {
            if (!prev.has(key)) return prev;
            const next = new Set(prev);
            next.delete(key);
            return next;
          });
        });
    },
    [mutateAsync],
  );

  const isSubmitting = useCallback(
    (item: Parameters<typeof requestMediaKey>[0]) => pendingKeys.has(requestMediaKey(item)),
    [pendingKeys],
  );

  return { submit, isSubmitting };
}
