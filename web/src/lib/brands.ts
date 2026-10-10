export type Brand = {
  slug: string;
  name: string;
  domain: string; // marketing sending domain
  color: string;
};

// Placeholder until the brands API lands in M1 — matches the brands in design.fig.
export const brands: Brand[] = [
  { slug: "brightside", name: "Brightside", domain: "news.brightside.co", color: "#f97316" },
  { slug: "kettle", name: "Kettle & Co", domain: "mail.kettleandco.com", color: "#10b981" },
  { slug: "nimbus", name: "Nimbus", domain: "updates.nimbusnotes.app", color: "#3b82f6" },
];

export function findBrand(slug: string | undefined): Brand | undefined {
  return brands.find((b) => b.slug === slug);
}

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

export function lastBrand(): Brand {
  let slug: string | null = null;
  try {
    slug = localStorage.getItem(LAST_BRAND_KEY);
  } catch {
    /* ignore */
  }
  return findBrand(slug ?? undefined) ?? brands[0]!;
}
