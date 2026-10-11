import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "./client";
import { withStepUp } from "./passkeys";

// Contacts API admin (S14). Mirrors api/internal/contactsapi/admin.go.

export type ApiKeyState = "pending_approval" | "active" | "expired" | "revoked";

export type ApiKey = {
  id: string;
  name: string;
  prefix: string;
  scopes: string[];
  ip_allowlist: string[];
  created_at: string;
  expires_at: string;
  last_used_at: string | null;
  revoked_at: string | null;
  state: ApiKeyState;
  approval?: { id: string; status: string } | null; // set when a second admin must approve
};

export type ConsentWording = { id: string; label: string; text: string; created_at: string; retired_at: string | null };

export type ApiCall = {
  id: number;
  key_name: string;
  at: string;
  method: string;
  path: string;
  status: number;
  ip: string;
  email_mask: string;
  error_code: string;
};

export type Repermission = { on: boolean; sending_ready: boolean; quarantined: number; sent_today: number; per_day: number };

export function useApiKeys(slug: string, enabled: boolean) {
  return useQuery({
    queryKey: ["api-keys", slug],
    queryFn: () => apiFetch<{ keys: ApiKey[]; scopes: string[] }>(`/admin/v1/brands/${slug}/api-keys`),
    enabled,
  });
}

export type NewKey = { name: string; scopes: string[]; ip_allowlist: string[]; expires_in_days: number };

export function useCreateApiKey(slug: string) {
  const qc = useQueryClient();
  return useMutation({
    // Creating a key is sensitive: the server asks for a passkey confirmation.
    mutationFn: (body: NewKey) =>
      withStepUp(() => apiFetch<{ key: ApiKey; secret: string }>(`/admin/v1/brands/${slug}/api-keys`, { method: "POST", body })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["api-keys", slug] });
      void qc.invalidateQueries({ queryKey: ["approvals"] });
    },
  });
}

export function useRevokeApiKey(slug: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => apiFetch<ApiKey>(`/admin/v1/brands/${slug}/api-keys/${id}/revoke`, { method: "POST" }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["api-keys", slug] }),
  });
}

export function useApiCalls(slug: string, enabled: boolean) {
  return useQuery({
    queryKey: ["api-calls", slug],
    queryFn: async () => (await apiFetch<{ calls: ApiCall[] }>(`/admin/v1/brands/${slug}/api-calls`)).calls,
    enabled,
    refetchInterval: 30_000,
  });
}

export function useWordings(slug: string) {
  return useQuery({
    queryKey: ["wordings", slug],
    queryFn: async () => (await apiFetch<{ wordings: ConsentWording[] }>(`/admin/v1/brands/${slug}/consent-wordings`)).wordings,
  });
}

export function useAddWording(slug: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: { label: string; text: string }) =>
      apiFetch<ConsentWording>(`/admin/v1/brands/${slug}/consent-wordings`, { method: "POST", body }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["wordings", slug] }),
  });
}

export function useRetireWording(slug: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => apiFetch<ConsentWording>(`/admin/v1/brands/${slug}/consent-wordings/${id}/retire`, { method: "POST" }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["wordings", slug] }),
  });
}

export function useRepermission(slug: string) {
  return useQuery({
    queryKey: ["repermission", slug],
    queryFn: () => apiFetch<Repermission>(`/admin/v1/brands/${slug}/repermission`),
  });
}

export function useSetRepermission(slug: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (on: boolean) => {
      const call = () => apiFetch<Repermission>(`/admin/v1/brands/${slug}/repermission`, { method: "PUT", body: { on } });
      return on ? withStepUp(call) : call(); // turning it on starts email
    },
    onSuccess: (d) => qc.setQueryData(["repermission", slug], d),
  });
}
