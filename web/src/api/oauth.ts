import { useMutation, useQuery } from "@tanstack/react-query";
import { apiFetch } from "./client";
import { withStepUp } from "./passkeys";

// An AI client's sign-in request (claude.ai, Claude Desktop). Mirrors api/internal/oauth.

export type OAuthGrant = {
  id: string;
  client_name: string;
  redirect_host: string;
  expires_at: string;
  status: "pending" | "approved" | "denied" | "expired";
  token_days: number;
};

export function useOAuthGrant(id: string) {
  return useQuery({
    queryKey: ["oauth-grant", id],
    queryFn: () => apiFetch<OAuthGrant>(`/admin/v1/oauth/grants/${id}`),
  });
}

// Approving needs a passkey; both answers say where to send the person back.
export function useDecideOAuthGrant(id: string) {
  return useMutation({
    mutationFn: (approve: boolean) => {
      const call = () =>
        apiFetch<{ redirect_to: string }>(
          `/admin/v1/oauth/grants/${id}/${approve ? "approve" : "deny"}`,
          { method: "POST" },
        );
      return approve ? withStepUp(call) : call();
    },
    onSuccess: (r) => window.location.assign(r.redirect_to),
  });
}
