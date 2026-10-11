import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { API_URL, apiFetch } from "./client";
import { withStepUp } from "./passkeys";

// Settings → AI agents. Mirrors api/internal/agents.

export type AgentToken = {
  id: string;
  name: string;
  prefix: string;
  created_at: string;
  expires_at: string;
  revoked_at: string | null;
  last_used_at: string | null;
  state: "active" | "expired" | "revoked";
};

// mcpURL is where agents connect: the API server's /mcp.
export const mcpURL = () => `${API_URL || window.location.origin}/mcp`;

export function useAgentTokens() {
  return useQuery({
    queryKey: ["agent-tokens"],
    queryFn: () => apiFetch<{ tokens: AgentToken[] }>("/admin/v1/agent-tokens").then((d) => d.tokens),
  });
}

export function useCreateAgentToken() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: { name: string; expires_in_days: number }) =>
      withStepUp(() => apiFetch<{ token: AgentToken; secret: string }>("/admin/v1/agent-tokens", { method: "POST", body })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["agent-tokens"] }),
  });
}

export function useRevokeAgentToken() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => apiFetch<{ token: AgentToken }>(`/admin/v1/agent-tokens/${id}/revoke`, { method: "POST" }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["agent-tokens"] }),
  });
}
