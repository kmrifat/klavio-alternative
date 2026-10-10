import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, apiFetch } from "./client";
import { withStepUp } from "./passkeys";
import type {
  AuditEntry,
  BrandDetail,
  BrandListItem,
  BrandRole,
  BrandRoles,
  CompanyRole,
  Me,
  Meta,
  Team,
} from "./types";

export function useMeta() {
  return useQuery({
    queryKey: ["meta"],
    queryFn: () => apiFetch<Meta>("/admin/v1/meta"),
    refetchInterval: 30_000,
  });
}

// useMe resolves to null when nobody is signed in.
export function useMe() {
  return useQuery({
    queryKey: ["me"],
    queryFn: async () => {
      try {
        return await apiFetch<Me>("/admin/v1/me");
      } catch (e) {
        if (e instanceof ApiError && e.status === 401) return null;
        throw e;
      }
    },
    staleTime: 60_000,
  });
}

export function useBrands() {
  return useQuery({
    queryKey: ["brands"],
    queryFn: async () => (await apiFetch<{ brands: BrandListItem[] }>("/admin/v1/brands")).brands,
  });
}

export function useBrand(slug: string | undefined) {
  return useQuery({
    queryKey: ["brand", slug],
    queryFn: () => apiFetch<BrandDetail>(`/admin/v1/brands/${slug}`),
    enabled: !!slug,
  });
}

export function useTeam() {
  return useQuery({ queryKey: ["team"], queryFn: () => apiFetch<Team>("/admin/v1/team") });
}

export function useAudit(enabled: boolean) {
  return useQuery({
    queryKey: ["audit"],
    queryFn: async () => (await apiFetch<{ entries: AuditEntry[] }>("/admin/v1/audit")).entries,
    enabled,
  });
}

export type NewBrand = {
  name: string;
  slug: string;
  color: string;
  postal_address: string;
  sending_domain: string;
  link_domain: string;
};

export function useCreateBrand() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (b: NewBrand) =>
      withStepUp(() => apiFetch<BrandDetail>("/admin/v1/brands", { method: "POST", body: b })),
    onSuccess: (d) => {
      qc.setQueryData(["brand", d.brand.slug], d);
      void qc.invalidateQueries({ queryKey: ["brands"] });
      void qc.invalidateQueries({ queryKey: ["me"] });
    },
  });
}

export function useUpdateBrand(slug: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (b: { name?: string; color?: string; postal_address?: string }) =>
      apiFetch<BrandDetail>(`/admin/v1/brands/${slug}`, { method: "PATCH", body: b }),
    onSuccess: (d) => {
      qc.setQueryData(["brand", slug], d);
      void qc.invalidateQueries({ queryKey: ["brands"] });
    },
  });
}

export function useCheckDNS(slug: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => apiFetch<BrandDetail>(`/admin/v1/brands/${slug}/dns-check`, { method: "POST" }),
    onSuccess: (d) => {
      qc.setQueryData(["brand", slug], d);
      void qc.invalidateQueries({ queryKey: ["brands"] });
    },
  });
}

export type InviteInput = { email: string; name: string; company_role: CompanyRole; brand_roles: BrandRoles };

export function useInvite() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (i: InviteInput) =>
      withStepUp(() =>
        apiFetch<{ invite_url: string; expires_at: string; awaiting_approval: boolean }>("/admin/v1/team/invites", {
          method: "POST",
          body: i,
        }),
      ),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["team"] }),
  });
}

export function useSetRoles() {
  const qc = useQueryClient();
  return useMutation({
    // brand_roles: every brand to change; "" removes access.
    mutationFn: ({ id, ...roles }: { id: string; company_role: CompanyRole; brand_roles: Record<string, BrandRole | ""> }) =>
      // 204 = applied; 202 = giving admin rights, waiting for a second admin.
      withStepUp(() =>
        apiFetch<{ message: string } | undefined>(`/admin/v1/team/members/${id}/roles`, { method: "PUT", body: roles }),
      ),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["team"] });
      void qc.invalidateQueries({ queryKey: ["approvals"] });
      void qc.invalidateQueries({ queryKey: ["audit"] });
      void qc.invalidateQueries({ queryKey: ["me"] });
    },
  });
}

export function useSignOut() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => apiFetch<void>("/admin/v1/auth/logout", { method: "POST" }),
    onSuccess: () => {
      qc.setQueryData(["me"], null);
      qc.removeQueries({ predicate: (q) => q.queryKey[0] !== "me" && q.queryKey[0] !== "meta" });
    },
  });
}
