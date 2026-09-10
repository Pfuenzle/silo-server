import type { PluginAdminForm, PluginAdminFormCondition, PluginAdminFormField } from "@/api/types";

export {
  buildSchemaValues,
  coerceFieldValue,
  parseFieldTypes,
  type FieldType,
} from "./schemaFormValues";

export type SchemaOption = { value: string; label: string };

export function effectiveValue(field: PluginAdminFormField, values: Record<string, unknown>): unknown {
  return values[field.key] !== undefined ? values[field.key] : field.default_value;
}

function stringify(value: unknown): string {
  if (typeof value === "boolean") return value ? "true" : "false";
  if (value === null || value === undefined) return "";
  return String(value);
}

export function evaluateShowWhen(
  conditions: PluginAdminFormCondition[] | undefined,
  values: Record<string, unknown>,
  fields?: PluginAdminFormField[],
): boolean {
  if (!conditions || conditions.length === 0) return true;
  return conditions.every((c) =>
    c.equals.includes(stringify(conditionValue(c.field, values, fields))),
  );
}

function conditionValue(
  key: string,
  values: Record<string, unknown>,
  fields?: PluginAdminFormField[],
): unknown {
  if (values[key] !== undefined) return values[key];
  return fields?.find((field) => field.key === key)?.default_value;
}

function isNumberControl(field: PluginAdminFormField): boolean {
  return field.control === "NUMBER";
}

function isEmpty(value: unknown): boolean {
  if (value === undefined || value === null) return true;
  if (typeof value === "string") return value.trim() === "";
  if (Array.isArray(value)) return value.length === 0;
  return false;
}

export function validateSchemaValues(
  descriptor: PluginAdminForm,
  values: Record<string, unknown>,
): Record<string, string> {
  const errors: Record<string, string> = {};
  for (const field of descriptor.fields) {
    if (!fieldIsVisible(descriptor, field, values)) continue;
    const raw = effectiveValue(field, values);
    if (field.required && isEmpty(raw)) {
      errors[field.key] = `${field.label || field.key} is required`;
      continue;
    }
    if (isEmpty(raw)) continue;
    const v = field.validation;
    if (typeof raw === "string") {
      let patternFailed = false;
      if (v?.pattern) {
        let re: RegExp | null = null;
        try {
          re = new RegExp(v.pattern);
        } catch {
          // A malformed plugin pattern is treated as "no pattern constraint".
          re = null;
        }
        patternFailed = re !== null && !re.test(raw);
      }
      if (patternFailed) {
        errors[field.key] = `${field.label || field.key} is invalid`;
      } else if (v?.min_length && raw.length < v.min_length) {
        errors[field.key] =
          `${field.label || field.key} must be at least ${v.min_length} characters`;
      } else if (v?.max_length && raw.length > v.max_length) {
        errors[field.key] =
          `${field.label || field.key} must be at most ${v.max_length} characters`;
      }
    }
    if (isNumberControl(field) && typeof raw === "string" && raw.trim() !== "") {
      const n = Number(raw);
      if (Number.isNaN(n)) errors[field.key] = `${field.label || field.key} must be a number`;
      else if (v?.has_min && n < (v.min ?? 0))
        errors[field.key] = `${field.label || field.key} must be ≥ ${v.min}`;
      else if (v?.has_max && n > (v.max ?? 0))
        errors[field.key] = `${field.label || field.key} must be ≤ ${v.max}`;
    }
  }
  return errors;
}

export function fieldIsVisible(
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
