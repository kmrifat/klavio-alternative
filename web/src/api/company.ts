import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "./client";
import { withStepUp } from "./passkeys";

// Settings → General. Mirrors api/internal/company.

export type Company = {
  name: string; // "" = not set yet
  display_name: string;
  installation: {
    version: string;
    env: "development" | "production";
    debug: boolean;
    app_url: string;
    passkey_domain: string;
    cloudflare_access: boolean;
  };
};

export function useCompany() {
  return useQuery({ queryKey: ["company"], queryFn: () => apiFetch<Company>("/admin/v1/company") });
}

export function useRenameCompany() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (name: string) => withStepUp(() => apiFetch<Company>("/admin/v1/company", { method: "PUT", body: { name } })),
    onSuccess: (c) => {
      qc.setQueryData(["company"], c);
      void qc.invalidateQueries({ queryKey: ["meta"] });
    },
  });
}
