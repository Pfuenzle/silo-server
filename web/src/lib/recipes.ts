import { api } from "../api/client";

export type Category =
  | "library_staples"
  | "personalized"
  | "discovery"
  | "editorial"
  | "seasonal"
  | "mood"
  | "hand_picked"
  | "social"
  | "custom";

export interface GalleryPreset {
  key: string;
  display_name: string;
  display_name_localized?: Record<string, string>;
  icon: string;
  description_short: string;
  description_short_localized?: Record<string, string>;
  description_long?: string;
  default_params: Record<string, unknown>;
}

export interface RecipeDefinition {
  type: string;
  category: Category;
  required_library_type?: string;
  presets: GalleryPreset[];
  avoid_duplicates: boolean;
  supports_rotation: boolean;
  admin_only: boolean;
}

export interface RecipeCatalogResponse {
  categories: Partial<Record<Category, RecipeDefinition[]>>;
}

export interface Candidate {
  value: string;
  display_name: string;
  subtitle?: string;
}

export interface PreviewRequest {
  section_type: string;
  config: Record<string, unknown>;
  item_limit?: number;
  library_id?: number;
  library_ids?: number[];
}

export interface PreviewResponse {
  items: Array<{ content_id: string; title?: string; poster_path?: string }>;
  total_count: number;
}

export async function fetchRecipeCatalog(): Promise<RecipeCatalogResponse> {
  return api<RecipeCatalogResponse>("/sections/recipes");
}

export function recipeAvailableForLibraries(
  definition: RecipeDefinition,
  libraries: readonly { readonly type?: string }[],
): boolean {
  return (
    definition.required_library_type === undefined ||
    libraries.some(
      (library) => library.type === definition.required_library_type,
    )
  );
}

export function filterRecipeCatalog(
  catalog: RecipeCatalogResponse,
  libraries: readonly { readonly type?: string }[],
): RecipeCatalogResponse {
  const categories = {} as Partial<Record<Category, RecipeDefinition[]>>;
  for (const [category, definitions] of Object.entries(catalog.categories) as [
    Category,
    RecipeDefinition[] | undefined,
  ][]) {
    categories[category] = definitions?.filter((definition) =>
      recipeAvailableForLibraries(definition, libraries),
    );
  }
  return { categories };
}

export async function fetchCandidates(
  recipeType: string,
): Promise<Candidate[]> {
  const body = await api<{ candidates: Candidate[] }>(
    `/sections/recipes/${encodeURIComponent(recipeType)}/candidates`,
  );
  return body.candidates;
}

export async function previewSection(
  req: PreviewRequest,
): Promise<PreviewResponse> {
  return api<PreviewResponse>("/admin/sections/preview", {
    method: "POST",
    body: JSON.stringify(req),
  });
}
