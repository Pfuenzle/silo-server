import { useState } from "react";

import { Button } from "@/components/ui/button";

type OAuthFailureProps = {
  readonly onRetry: () => void;
};

export function OAuthFailure({ onRetry }: OAuthFailureProps) {
  const [retrying, setRetrying] = useState(false);

  function handleRetry() {
    if (retrying) {
      return;
    }
    setRetrying(true);
    onRetry();
  }

  return (
    <div className="border-destructive/30 bg-destructive/10 rounded-md border p-4" role="alert">
      <div className="space-y-3">
        <div className="space-y-1">
          <h2 className="text-foreground text-base font-semibold">We couldn&apos;t sign you in</h2>
          <p className="text-muted-foreground text-sm">Return to sign in and try again.</p>
        </div>
        <Button
          type="button"
          className="w-full"
          disabled={retrying}
          autoFocus
          onClick={handleRetry}
        >
          {retrying ? "Returning to sign in..." : "Try again"}
        </Button>
      </div>
    </div>
  );
}
