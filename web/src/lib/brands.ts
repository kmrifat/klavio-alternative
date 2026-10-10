import type { BrandListItem } from "../api/types";

const LAST_BRAND_KEY = "lastBrand";

// Remembered so brand links still work from the company view. Storage can be
// unavailable (private mode), so every access is guarded.
export function rememberBrand(slug: string) {
  try {
    localStorage.setItem(LAST_BRAND_KEY, slug);
  } catch {
    /* ignore */
  }
}

// lastBrand picks the remembered brand if the user can still see it, else the first.
export function lastBrand(brands: BrandListItem[]): BrandListItem | undefined {
  let slug: string | null = null;
  try {
    slug = localStorage.getItem(LAST_BRAND_KEY);
  } catch {
    /* ignore */
  }
  return brands.find((b) => b.slug === slug) ?? brands[0];
}
