// @vitest-environment jsdom

import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { SearchResultsView } from "./Requests";

describe("SearchResultsView", () => {
  it("shows a retry action when the provider search fails", () => {
    const onRetry = vi.fn();

    render(
      <SearchResultsView
        query="Inception"
        mediaType="all"
        page={1}
        onPageChange={vi.fn()}
        isLoading={false}
        isError
        totalPages={0}
        totalResults={0}
        results={[]}
        isSubmitting={false}
        onRequest={vi.fn()}
        onRetry={onRetry}
      />,
    );

    const retry = screen.getByRole("button", { name: "Retry search" });
    expect(retry).toBeVisible();
    retry.click();
    expect(onRetry).toHaveBeenCalledOnce();
  });
});
