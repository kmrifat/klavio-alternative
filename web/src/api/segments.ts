import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "./client";
import type { Contact, Tier } from "./types";

// Segments (S12). Mirrors api/internal/segments.

export type FieldKind = "text" | "enum" | "tag" | "topic" | "date" | "country";

export type SegmentField = { key: string; label: string; kind: FieldKind; ops: string[]; options?: string[] };

export type Condition = { field: string; op: string; value?: unknown };
export type RuleGroup = { match: "all" | "any"; conditions: Condition[] };
export type Rules = { match: "all" | "any"; groups: RuleGroup[] };

export type SegmentCounts = { matching: number; mailable: number; tiers: Record<Tier, number> };

export type Segment = {
  id: string;
  name: string;
  rules: Rules;
  created_at: string;
  updated_at: string;
  counts: SegmentCounts | null; // last count; null = never counted
  counted_at: string | null;
};

export function useSegmentFields() {
  return useQuery({
    queryKey: ["segment-fields"],
    queryFn: async () => (await apiFetch<{ fields: SegmentField[] }>("/admin/v1/segments/fields")).fields,
    staleTime: Infinity,
  });
}

export function useSegments(slug: string) {
  return useQuery({
    queryKey: ["segments", slug],
    queryFn: async () => (await apiFetch<{ segments: Segment[] }>(`/admin/v1/brands/${slug}/segments`)).segments,
  });
}

export function useSegment(slug: string, id: string | undefined) {
  return useQuery({
    queryKey: ["segment", slug, id],
    queryFn: () => apiFetch<Segment>(`/admin/v1/brands/${slug}/segments/${id}`),
    enabled: !!id,
  });
}

// useSegmentPreview counts live while editing; key is the debounced rules.
export function useSegmentPreview(slug: string, rules: Rules | null) {
  return useQuery({
    queryKey: ["segment-preview", slug, rules],
    queryFn: () =>
      apiFetch<{ counts: SegmentCounts; sample: Contact[] }>(`/admin/v1/brands/${slug}/segments/preview`, { method: "POST", body: { rules } }),
    enabled: rules !== null,
    placeholderData: (prev) => prev,
    retry: false,
  });
}

export function useSaveSegment(slug: string, id: string | undefined) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: { name: string; rules: Rules }) =>
      apiFetch<Segment>(id ? `/admin/v1/brands/${slug}/segments/${id}` : `/admin/v1/brands/${slug}/segments`, {
        method: id ? "PUT" : "POST",
        body,
      }),
    onSuccess: (s) => {
      qc.setQueryData(["segment", slug, s.id], s);
      void qc.invalidateQueries({ queryKey: ["segments", slug] });
    },
  });
}

export function useDeleteSegment(slug: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => apiFetch<void>(`/admin/v1/brands/${slug}/segments/${id}`, { method: "DELETE" }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["segments", slug] }),
  });
}

export const opLabel: Record<string, string> = {
  equals: "is",
  not_equals: "is not",
  contains: "contains",
  starts_with: "starts with",
  ends_with: "ends with",
  is_empty: "is empty",
  is_not_empty: "is not empty",
  is: "is",
  is_not: "is not",
  in: "is any of",
  not_in: "is none of",
  has: "has",
  has_not: "doesn't have",
  subscribed: "subscribed to",
  not_subscribed: "not subscribed to",
  within_days: "in the last … days",
  older_than_days: "not in the last … days",
  before: "before",
  after: "after",
};
