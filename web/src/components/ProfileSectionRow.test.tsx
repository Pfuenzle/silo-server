import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi } from "vitest";
import ProfileSectionRow from "./ProfileSectionRow";

describe("ProfileSectionRow", () => {
  it("shows server-default badge for admin sections and a Hide button", async () => {
    const onHide = vi.fn();
    render(
      <ProfileSectionRow
        kind="server-default"
        title="Continue Watching"
        sectionType="continue_watching"
        hidden={false}
        onHide={onHide}
        onShow={() => {}}
        onEdit={() => {}}
        onDelete={() => {}}
        onMoveUp={() => {}}
        onMoveDown={() => {}}
        canMoveUp={false}
        canMoveDown={true}
      />,
    );
    expect(screen.getByText(/server default/i)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /hide/i }));
    expect(onHide).toHaveBeenCalled();
  });

  it("shows yours badge for user-added and a Delete button", async () => {
    const onDelete = vi.fn();
    render(
      <ProfileSectionRow
        kind="yours"
        title="Hidden Gems"
        sectionType="hidden_gems"
        hidden={false}
        onHide={() => {}}
        onShow={() => {}}
        onEdit={() => {}}
        onDelete={onDelete}
        onMoveUp={() => {}}
        onMoveDown={() => {}}
        canMoveUp={true}
        canMoveDown={false}
      />,
    );
    expect(screen.getByText(/yours/i)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /delete/i }));
    expect(onDelete).toHaveBeenCalled();
  });
});
