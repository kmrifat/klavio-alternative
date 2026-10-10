import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "./client";
import { withStepUp } from "./passkeys";

// Shapes and hooks for api/internal/templates: the brand layout, the link
// allowlist and templates (S13).

export type Issue = {
  code: string;
  severity: "error" | "warning";
  message: string;
  where?: "template" | "layout";
  line?: number;
};

export type MergeTag = { name: string; description: string; url: boolean };

export type Layout = { mjml: string; version: number; is_default: boolean; updated_at: string | null };

// Returned after a layout or allowlist change: every template is recompiled
// in the background, and shows as stale until then.
type Rebuilt = { templates_queued?: number };

export type LayoutResponse = Rebuilt & { layout: Layout; preview_html: string; issues: Issue[]; tags: MergeTag[] };

export type AllowedDomain = { domain: string; added_by: string | null; created_at: string };

export type AllowlistResponse = Rebuilt & { domains: AllowedDomain[]; brand_domains: string[] };

export type TemplateSummary = {
  id: string;
  name: string;
  error_count: number;
  warning_count: number;
  stale: boolean;
  updated_at: string;
  updated_by_name: string | null;
};

export type Template = Omit<TemplateSummary, "updated_by_name"> & {
  mjml: string;
  html: string;
  text: string;
  issues: Issue[];
  layout_version: number;
  version: number;
  created_at: string;
};

export type TemplateResponse = { template: Template; preview_html: string; preview_text: string; tags: MergeTag[] };

export type PreviewResponse = { preview_html: string; preview_text: string; issues: Issue[] };

const brandPath = (slug: string) => `/admin/v1/brands/${slug}`;

export function useLayout(slug: string) {
  return useQuery({ queryKey: ["layout", slug], queryFn: () => apiFetch<LayoutResponse>(`${brandPath(slug)}/layout`) });
}

export type LayoutApprovalPending = { approval: { id: string; status: string; expires_at: string } };

export function useSaveLayout(slug: string) {
  const qc = useQueryClient();
  return useMutation({
    // A second admin approves layout changes: the answer is then the
    // pending approval (202), and the layout stays as it was until then.
    mutationFn: (l: { mjml: string; version: number }) =>
      withStepUp(() => apiFetch<LayoutResponse | LayoutApprovalPending>(`${brandPath(slug)}/layout`, { method: "PUT", body: l })),
    onSuccess: (d) => {
      if ("approval" in d) {
        void qc.invalidateQueries({ queryKey: ["approvals"] });
        return;
      }
      qc.setQueryData(["layout", slug], d);
      void qc.invalidateQueries({ queryKey: ["templates", slug] });
    },
  });
}

export function useAllowlist(slug: string) {
  return useQuery({ queryKey: ["allowlist", slug], queryFn: () => apiFetch<AllowlistResponse>(`${brandPath(slug)}/link-allowlist`) });
}

export function useAllowDomain(slug: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (domain: string) =>
      withStepUp(() => apiFetch<AllowlistResponse>(`${brandPath(slug)}/link-allowlist`, { method: "POST", body: { domain } })),
    onSuccess: (d) => {
      qc.setQueryData(["allowlist", slug], d);
      void qc.invalidateQueries({ queryKey: ["templates", slug] });
    },
  });
}

export function useDisallowDomain(slug: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (domain: string) =>
      withStepUp(() =>
        apiFetch<AllowlistResponse>(`${brandPath(slug)}/link-allowlist/${encodeURIComponent(domain)}`, { method: "DELETE" }),
      ),
    onSuccess: (d) => {
      qc.setQueryData(["allowlist", slug], d);
      void qc.invalidateQueries({ queryKey: ["templates", slug] });
    },
  });
}

export function useTemplates(slug: string) {
  return useQuery({
    queryKey: ["templates", slug],
    queryFn: async () => (await apiFetch<{ templates: TemplateSummary[] }>(`${brandPath(slug)}/templates`)).templates,
  });
}

export function useTemplate(slug: string, id: string | undefined) {
  return useQuery({
    queryKey: ["templates", slug, id],
    queryFn: () => apiFetch<TemplateResponse>(`${brandPath(slug)}/templates/${id}`),
    enabled: !!id,
  });
}

export type TemplateInput = { name: string; mjml: string; version?: number };

export function useSaveTemplate(slug: string, id: string | undefined) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (t: TemplateInput) =>
      id
        ? apiFetch<TemplateResponse>(`${brandPath(slug)}/templates/${id}`, { method: "PUT", body: t })
        : apiFetch<TemplateResponse>(`${brandPath(slug)}/templates`, { method: "POST", body: t }),
    onSuccess: (d) => {
      qc.setQueryData(["templates", slug, d.template.id], d);
      void qc.invalidateQueries({ queryKey: ["templates", slug], exact: true });
    },
  });
}

export function useArchiveTemplate(slug: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => apiFetch<void>(`${brandPath(slug)}/templates/${id}`, { method: "DELETE" }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["templates", slug] }),
  });
}

// Previews compile without saving; the editor calls them as you type.
export const previewTemplate = (slug: string, mjml: string) =>
  apiFetch<PreviewResponse>(`${brandPath(slug)}/templates/preview`, { method: "POST", body: { mjml } });

export const previewLayout = (slug: string, mjml: string) =>
  apiFetch<LayoutResponse>(`${brandPath(slug)}/layout/preview`, { method: "POST", body: { mjml } });
