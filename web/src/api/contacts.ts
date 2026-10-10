import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { API_URL, ApiError, apiFetch } from "./client";
import { withStepUp } from "./passkeys";
import type { Contact, ContactDetail, ContactStats, ImportConsent, ImportMapping, ImportView, Tier, Topic } from "./types";

export type ContactFilters = { tiers: Tier[]; status: string; tag: string; q: string };

export function useContacts(slug: string, f: ContactFilters, enabled = true) {
  return useInfiniteQuery({
    queryKey: ["contacts", slug, f],
    enabled,
    initialPageParam: "",
    queryFn: ({ pageParam }) => {
      const p = new URLSearchParams();
      if (f.tiers.length) p.set("tier", f.tiers.join(","));
      if (f.status) p.set("status", f.status);
      if (f.tag) p.set("tag", f.tag);
      if (f.q) p.set("q", f.q);
      if (pageParam) p.set("after", pageParam);
      return apiFetch<{ contacts: Contact[]; next_after: string }>(`/admin/v1/brands/${slug}/contacts?${p}`);
    },
    getNextPageParam: (last) => last.next_after || undefined,
  });
}

export function useContactStats(slug: string) {
  return useQuery({
    queryKey: ["contact-stats", slug],
    queryFn: () => apiFetch<ContactStats>(`/admin/v1/brands/${slug}/contacts/stats`),
  });
}

export function useContact(slug: string, id: string | null) {
  return useQuery({
    queryKey: ["contact", slug, id],
    queryFn: () => apiFetch<ContactDetail>(`/admin/v1/brands/${slug}/contacts/${id}`),
    enabled: !!id,
  });
}

function useInvalidateContacts(slug: string) {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: ["contacts", slug] });
    void qc.invalidateQueries({ queryKey: ["contact-stats", slug] });
    void qc.invalidateQueries({ queryKey: ["contact", slug] });
    void qc.invalidateQueries({ queryKey: ["topics", slug] });
  };
}

export type NewContact = {
  email: string;
  first_name: string;
  last_name: string;
  tags: string[];
  topic_ids: string[];
  consent: { basis: string; source: string; consented_at: string };
};

export function useAddContact(slug: string) {
  const invalidate = useInvalidateContacts(slug);
  return useMutation({
    mutationFn: (c: NewContact) => apiFetch<Contact>(`/admin/v1/brands/${slug}/contacts`, { method: "POST", body: c }),
    onSuccess: invalidate,
  });
}

export function useUnsubscribe(slug: string) {
  const invalidate = useInvalidateContacts(slug);
  return useMutation({
    mutationFn: ({ id, note }: { id: string; note: string }) =>
      apiFetch<void>(`/admin/v1/brands/${slug}/contacts/${id}/unsubscribe`, { method: "POST", body: { note } }),
    onSuccess: invalidate,
  });
}

export function useSuppress(slug: string) {
  const invalidate = useInvalidateContacts(slug);
  return useMutation({
    mutationFn: ({ id, note }: { id: string; note: string }) =>
      apiFetch<void>(`/admin/v1/brands/${slug}/contacts/${id}/suppress`, { method: "POST", body: { note } }),
    onSuccess: invalidate,
  });
}

export function useTopics(slug: string) {
  return useQuery({
    queryKey: ["topics", slug],
    queryFn: async () => (await apiFetch<{ topics: Topic[] }>(`/admin/v1/brands/${slug}/topics`)).topics,
  });
}

export function useAddTopic(slug: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (name: string) => apiFetch<Topic>(`/admin/v1/brands/${slug}/topics`, { method: "POST", body: { name } }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["topics", slug] }),
  });
}

// --- imports ---

const importPath = (slug: string, id?: string) => `/admin/v1/brands/${slug}/imports${id ? `/${id}` : ""}`;

// The file upload is multipart, so it skips apiFetch's JSON encoding.
export async function uploadImport(slug: string, file: File): Promise<ImportView> {
  const form = new FormData();
  form.append("file", file);
  const res = await fetch(`${API_URL}${importPath(slug)}`, { method: "POST", credentials: "include", body: form });
  const data = (await res.json().catch(() => ({}))) as ImportView & { error?: { code: string; message: string } };
  if (!res.ok) throw new ApiError(res.status, data.error?.code ?? "http_error", data.error?.message ?? "Upload failed");
  return data;
}

export function useImport(slug: string, id: string | undefined) {
  return useQuery({
    queryKey: ["import", slug, id],
    queryFn: () => apiFetch<ImportView>(importPath(slug, id)),
    enabled: !!id,
    // Poll while a background step is running.
    refetchInterval: (q) => {
      const s = q.state.data?.import.status;
      return s === "checking" || s === "importing" ? 2000 : false;
    },
  });
}

export type ImportSetup = { mapping: ImportMapping; consent: ImportConsent; topic_ids: string[] };

export function useImportActions(slug: string, id: string) {
  const qc = useQueryClient();
  const invalidateContacts = useInvalidateContacts(slug);
  const set = (v: ImportView) => qc.setQueryData(["import", slug, id], v);
  return {
    setup: useMutation({
      mutationFn: (s: ImportSetup) => apiFetch<ImportView>(importPath(slug, id), { method: "PUT", body: s }),
      onSuccess: set,
    }),
    check: useMutation({
      mutationFn: () => apiFetch<ImportView>(`${importPath(slug, id)}/check`, { method: "POST" }),
      onSuccess: set,
    }),
    commit: useMutation({
      mutationFn: () => withStepUp(() => apiFetch<ImportView>(`${importPath(slug, id)}/commit`, { method: "POST" })),
      onSuccess: (v) => {
        set(v);
        invalidateContacts();
      },
    }),
    cancel: useMutation({
      mutationFn: () => apiFetch<ImportView>(`${importPath(slug, id)}/cancel`, { method: "POST" }),
      onSuccess: set,
    }),
  };
}
