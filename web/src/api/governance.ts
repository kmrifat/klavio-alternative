import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { API_URL, ApiError, apiFetch } from "./client";
import { withStepUp } from "./passkeys";
import type { Alert, ApprovalItem, AuditEntry, ExportItem, LimitChangeResult, SafetyLimit } from "./types";

export type ApprovalView = "decide" | "mine" | "recent";

export function useApprovals(view: ApprovalView) {
  return useQuery({
    queryKey: ["approvals", view],
    queryFn: async () => (await apiFetch<{ approvals: ApprovalItem[] }>(`/admin/v1/approvals?view=${view}`)).approvals,
    refetchInterval: 30_000,
  });
}

export function useDecide() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, decision, note }: { id: string; decision: "approve" | "reject"; note: string }) => {
      const call = () => apiFetch<ApprovalItem>(`/admin/v1/approvals/${id}/${decision}`, { method: "POST", body: { note } });
      // Approving acts with your authority: the server asks for a passkey confirmation.
      return decision === "approve" ? withStepUp(call) : call();
    },
    onSuccess: () => {
      for (const k of ["approvals", "safety-limits", "team", "exports", "audit-log", "me"]) void qc.invalidateQueries({ queryKey: [k] });
    },
  });
}

export function useCancelApproval() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => apiFetch<void>(`/admin/v1/approvals/${id}/cancel`, { method: "POST" }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["approvals"] }),
  });
}

export function useSafetyLimits() {
  return useQuery({
    queryKey: ["safety-limits"],
    queryFn: async () => (await apiFetch<{ limits: SafetyLimit[] }>("/admin/v1/safety-limits")).limits,
  });
}

export function useChangeLimits() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: { changes: Record<string, number>; reason: string }) =>
      withStepUp(() => apiFetch<LimitChangeResult>("/admin/v1/safety-limits", { method: "PUT", body })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["safety-limits"] });
      void qc.invalidateQueries({ queryKey: ["approvals"] });
    },
  });
}

export type AuditFilters = { brand: string; actor: string; action: string; since: string; until: string };

export function useAuditLog(f: AuditFilters) {
  return useInfiniteQuery({
    queryKey: ["audit-log", f],
    initialPageParam: 0,
    queryFn: ({ pageParam }) => {
      const p = new URLSearchParams();
      for (const [k, v] of Object.entries(f)) if (v) p.set(k, v);
      if (pageParam) p.set("before", String(pageParam));
      return apiFetch<{ entries: AuditEntry[]; next_before: number }>(`/admin/v1/audit?${p}`);
    },
    getNextPageParam: (last) => last.next_before || undefined,
  });
}

export function useMyExports() {
  return useQuery({
    queryKey: ["exports"],
    queryFn: async () => (await apiFetch<{ exports: ExportItem[] }>("/admin/v1/exports")).exports,
  });
}

export function useRequestExport(slug: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: { filter: { tiers: string[]; status: string; tag: string }; reason: string }) =>
      apiFetch<{ export: ExportItem; requester_frozen: boolean }>(`/admin/v1/brands/${slug}/exports`, { method: "POST", body }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["exports"] });
      void qc.invalidateQueries({ queryKey: ["approvals"] });
    },
  });
}

// downloadExport saves the CSV through the browser (the session cookie authenticates).
export async function downloadExport(id: string): Promise<void> {
  const res = await fetch(`${API_URL}/admin/v1/exports/${id}/download`, { credentials: "include" });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: { code: string; message: string } };
    throw new ApiError(res.status, data.error?.code ?? "http_error", data.error?.message ?? "Download failed");
  }
  const url = URL.createObjectURL(await res.blob());
  const a = document.createElement("a");
  a.href = url;
  a.download = `contacts-export-${id.slice(0, 8)}.csv`;
  a.click();
  URL.revokeObjectURL(url);
}

export function useAlerts(enabled: boolean, openOnly = true) {
  return useQuery({
    queryKey: ["alerts", openOnly],
    queryFn: async () => (await apiFetch<{ alerts: Alert[] }>(`/admin/v1/alerts${openOnly ? "?open=true" : ""}`)).alerts,
    enabled,
    refetchInterval: 60_000,
  });
}

export function useAcknowledgeAlert() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => apiFetch<void>(`/admin/v1/alerts/${id}/acknowledge`, { method: "POST" }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["alerts"] }),
  });
}

export function useUnfreeze() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => withStepUp(() => apiFetch<void>(`/admin/v1/team/members/${id}/unfreeze`, { method: "POST" })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["team"] });
      void qc.invalidateQueries({ queryKey: ["alerts"] });
    },
  });
}
