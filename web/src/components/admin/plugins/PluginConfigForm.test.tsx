// @vitest-environment jsdom

import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import type { PluginConfigSchema } from "@/api/types";

import { PluginConfigForm } from "./PluginConfigForm";

const schema: PluginConfigSchema = {
  key: "account",
  title: "Account",
  json_schema: "{}",
  required: true,
  admin_form: {
    fields: [
      {
        key: "api_key",
        label: "API Key",
        control: "PASSWORD",
        required: false,
        secret: true,
        multiline: false,
      },
      {
        key: "region",
        label: "Region",
        control: "TEXT",
        required: false,
        secret: false,
        multiline: false,
      },
    ],
  },
};

describe("PluginConfigForm secrets", () => {
  it("derives a form when a plugin only supplies JSON Schema", () => {
    render(
      <PluginConfigForm
        schema={{
          key: "server",
          title: "Server",
          json_schema: JSON.stringify({
            type: "object",
            properties: {
              base_url: { type: "string", title: "Base URL" },
              api_key: { type: "string", format: "password" },
            },
            required: ["base_url", "api_key"],
          }),
          required: true,
        }}
        onSave={vi.fn()}
      />,
    );

    expect(screen.getByLabelText("Base URL")).toBeInTheDocument();
    expect(screen.getByLabelText("Api Key")).toHaveAttribute("type", "password");
  });

  it("edits object-array config fields as JSON and submits parsed values", async () => {
    const onSave = vi.fn();
    render(
      <PluginConfigForm
        schema={{
          key: "listenarr",
          title: "Listenarr",
          json_schema: JSON.stringify({
            type: "object",
            properties: {
              path_mappings: {
                type: "array",
                items: {
                  type: "object",
                  properties: {
                    source: { type: "string" },
                    destination: { type: "string" },
                  },
                },
              },
            },
            required: ["path_mappings"],
          }),
          required: true,
        }}
        value={{ path_mappings: [{ source: "/imports", destination: "/library" }] }}
        onSave={onSave}
      />,
    );

    const field = screen.getByLabelText("Path Mappings");
    expect(field).toHaveValue(`[
  {
    "source": "/imports",
    "destination": "/library"
  }
]`);
    fireEvent.change(field, { target: { value: '[{"source":"/new","destination":"/library"}]' } });
    await userEvent.click(screen.getByRole("button", { name: "Save config" }));

    expect(onSave).toHaveBeenLastCalledWith(
      "listenarr",
      { path_mappings: [{ source: "/new", destination: "/library" }] },
      [],
      [],
    );
  });

  it("shows redacted saved state and only clears through an explicit action", async () => {
    const onSave = vi.fn();
    render(
      <PluginConfigForm
        schema={schema}
        value={{ region: "us-east" }}
        configuredSecrets={["api_key"]}
        onSave={onSave}
      />,
    );

    expect(screen.getByLabelText("API Key")).toHaveAttribute(
      "placeholder",
      "Saved secret — leave blank to keep",
    );
    expect(screen.getByText("API Key: saved")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Save config" }));
    expect(onSave).toHaveBeenLastCalledWith(
      "account",
      expect.objectContaining({ region: "us-east" }),
      [],
      [],
    );

    await userEvent.click(screen.getByRole("button", { name: "Clear saved secret" }));
    await userEvent.click(screen.getByRole("button", { name: "Save config" }));
    expect(onSave).toHaveBeenLastCalledWith(
      "account",
      expect.objectContaining({ region: "us-east" }),
      ["api_key"],
      [],
    );
  });

  it("does not offer to clear a required saved secret into an invalid config", () => {
    const requiredSchema: PluginConfigSchema = {
      ...schema,
      admin_form: {
        ...schema.admin_form!,
        fields: schema.admin_form!.fields.map((field) =>
          field.key === "api_key" ? { ...field, required: true } : field,
        ),
      },
    };
    render(
      <PluginConfigForm
        schema={requiredSchema}
        value={{ region: "us-east" }}
        configuredSecrets={["api_key"]}
        onSave={vi.fn()}
      />,
    );

    expect(screen.getByText("API Key: saved (required)")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Clear saved secret" })).not.toBeInTheDocument();
  });

  it("keeps the submitted snapshot immutable while a save is pending", () => {
    render(
      <PluginConfigForm
        schema={schema}
        value={{ region: "us-east" }}
        configuredSecrets={["api_key"]}
        onSave={vi.fn()}
        isSaving
      />,
    );

    expect(screen.getByLabelText("Region")).toBeDisabled();
    expect(screen.getByRole("button", { name: "Save config" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Clear saved secret" })).toBeDisabled();
  });

  it("tests the exact draft including staged secret removals", async () => {
    const onTest = vi.fn().mockResolvedValue({
      success: false,
      message: "API key is required",
    });
    render(
      <PluginConfigForm
        schema={schema}
        value={{ region: "us-east" }}
        configuredSecrets={["api_key"]}
        onSave={vi.fn()}
        onTest={onTest}
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: "Clear saved secret" }));
    await userEvent.click(screen.getByRole("button", { name: "Check Connection" }));

    expect(onTest).toHaveBeenCalledWith(
      "account",
      expect.objectContaining({ region: "us-east" }),
      ["api_key"],
      [],
    );
  });

  it("explicitly clears a saved public field when the form value is emptied", async () => {
    const onSave = vi.fn();
    const onTest = vi.fn().mockResolvedValue({ success: true, message: "Connection successful." });
    render(
      <PluginConfigForm
        schema={schema}
        value={{ region: "us-east" }}
        onSave={onSave}
        onTest={onTest}
      />,
    );

    await userEvent.clear(screen.getByLabelText("Region"));
    await userEvent.click(screen.getByRole("button", { name: "Save config" }));
    expect(onSave).toHaveBeenCalledWith("account", {}, [], ["region"]);

    await userEvent.click(screen.getByRole("button", { name: "Check Connection" }));
    expect(onTest).toHaveBeenCalledWith("account", {}, [], ["region"]);
  });
});
