import type { PluginAdminForm, PluginAdminFormField } from "@/api/types";

import { evaluateShowWhen } from "./schemaFormUtils";

function fieldIsVisible(
  descriptor: PluginAdminForm,
  field: PluginAdminFormField,
  values: Record<string, unknown>,
): boolean {
  if (!evaluateShowWhen(field.show_when, values, descriptor.fields)) return false;
  const containingSections = (descriptor.sections ?? []).filter((section) =>
    section.field_keys.includes(field.key),
  );
  return (
    containingSections.length === 0 ||
    containingSections.some((section) =>
      evaluateShowWhen(section.show_when, values, descriptor.fields),
    )
  );
}

function parseJSONValue(value: string): unknown {
  try {
    return JSON.parse(value);
  } catch {
    return undefined;
  }
}

function coerceNumericString(value: unknown): unknown {
  return typeof value === "string" && /^-?\d+$/.test(value) ? Number(value) : value;
}

function coerceNumberString(value: unknown): unknown {
  if (typeof value !== "string") return value;
  const trimmed = value.trim();
  if (trimmed === "" || !/^-?\d+(\.\d+)?$/.test(trimmed)) return value;
  const numberValue = Number(trimmed);
  return Number.isNaN(numberValue) ? value : numberValue;
}

function coerceBoolean(value: unknown): boolean {
  if (typeof value === "boolean") return value;
  if (typeof value === "string") return value.trim().toLowerCase() === "true";
  return Boolean(value);
}

export type FieldType =
  | "string"
  | "integer"
  | "number"
  | "boolean"
  | "array"
  | "array:bool"
  | "array:int"
  | "array:num";

export function parseFieldTypes(jsonSchema: string | undefined | null): Record<string, FieldType> {
  if (!jsonSchema) return {};
  let parsed: unknown;
  try {
    parsed = JSON.parse(jsonSchema);
  } catch {
    return {};
  }
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return {};
  const props = (parsed as { properties?: unknown }).properties;
  if (!props || typeof props !== "object" || Array.isArray(props)) return {};

  const out: Record<string, FieldType> = {};
  for (const [key, value] of Object.entries(props as Record<string, unknown>)) {
    if (!value || typeof value !== "object") continue;
    const type = (value as { type?: unknown }).type;
    if (type === "array") {
      const items = (value as { items?: unknown }).items;
      const itemType = items && typeof items === "object" ? (items as { type?: unknown }).type : undefined;
      if (itemType === "integer") out[key] = "array:int";
      else if (itemType === "number") out[key] = "array:num";
      else if (itemType === "boolean") out[key] = "array:bool";
      else out[key] = "array";
    } else if (type === "string" || type === "integer" || type === "number" || type === "boolean") {
      out[key] = type;
    }
  }
  return out;
}

export function coerceFieldValue(
  field: PluginAdminFormField,
  raw: unknown,
  fieldType?: FieldType,
): unknown {
  if (fieldType !== undefined) {
    switch (fieldType) {
      case "boolean":
        return coerceBoolean(raw);
      case "integer":
      case "number": {
        if (typeof raw === "number") return raw;
        if (typeof raw === "string") {
          if (raw.trim() === "") return undefined;
          const numberValue = Number(raw);
          return Number.isNaN(numberValue) ? raw : numberValue;
        }
        return raw;
      }
      case "string":
        return typeof raw === "string" ? (raw.trim() === "" ? undefined : raw) : raw;
      case "array":
      case "array:bool":
      case "array:int":
      case "array:num": {
        const values = Array.isArray(raw) ? raw : [];
        if (fieldType === "array") return values;
        if (fieldType === "array:bool") return values.map(coerceBoolean);
        if (fieldType === "array:int") return values.map(coerceNumericString);
        return values.map(coerceNumberString);
      }
    }
  }

  if (field.control === "SWITCH") return coerceBoolean(raw);
  if (field.control === "MULTI_SELECT") {
    const values = Array.isArray(raw) ? raw : [];
    return values.map(coerceNumericString);
  }
  if (field.control === "SELECT" && field.dynamic_options) {
    if (typeof raw === "string" && raw.trim() === "") return undefined;
    return coerceNumericString(raw);
  }
  if (field.control === "NUMBER") {
    if (typeof raw === "number") return raw;
    if (typeof raw === "string" && raw.trim() !== "") {
      const numberValue = Number(raw);
      return Number.isNaN(numberValue) ? raw : numberValue;
    }
    return undefined;
  }
  if (typeof raw === "string") {
    const trimmed = raw.trim();
    if (field.control === "TEXTAREA" && field.placeholder?.startsWith("[{")) {
      const parsed = parseJSONValue(trimmed);
      return parsed === undefined ? raw : parsed;
    }
    return trimmed === "" ? undefined : raw;
  }
  return raw;
}

export function effectiveValue(field: PluginAdminFormField, values: Record<string, unknown>): unknown {
  return values[field.key] !== undefined ? values[field.key] : field.default_value;
}

export function buildSchemaValues(
  descriptor: PluginAdminForm,
  draft: Record<string, unknown>,
  fieldTypes?: Record<string, FieldType>,
): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const field of descriptor.fields) {
    if (!fieldIsVisible(descriptor, field, draft)) continue;
    const rawSource = draft[field.key] !== undefined ? draft[field.key] : field.default_value;
    const coerced = coerceFieldValue(field, rawSource, fieldTypes?.[field.key]);
    if (coerced !== undefined) out[field.key] = coerced;
  }
  return out;
}
