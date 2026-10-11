import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "./client";
import { withStepUp } from "./passkeys";
import type { Contact } from "./types";

// GDPR erasure (Settings → Data & privacy). Mirrors api/internal/privacy.

export type ErasureSummary = {
  brands: string[];
  contacts_deleted: number;
  clicks_blanked: number;
  opens_blanked: number;
  events_blanked: number;
  send_errors_blanked: number;
  api_replies_deleted: number;
  imports_scrubbed: number;
  exports_deleted: number;
  audit_entries_redacted: number;
  consent_records_kept: number;
  previous_suppression?: string;
};

export type Erasure = {
  id: string;
  contact_ref: string;
  reference: string;
  summary: ErasureSummary;
  erased_by_name: string;
  erased_at: string;
};

export type PersonData = {
  contact_ref: string;
  contacts: { brand_slug: string; brand_name: string; brand_color: string; status: Contact["status"]; created_at: string }[];
  suppression: string | null;
  erasures: Erasure[];
  sends: number;
  clicks: number;
  opens: number;
  events: number;
  consent_records: number;
  audit_entries: number;
};

export function useErasures(enabled = true) {
  return useQuery({
    queryKey: ["erasures"],
    queryFn: () => apiFetch<{ erasures: Erasure[] }>("/admin/v1/erasures").then((d) => d.erasures),
    enabled,
  });
}

// A POST, so the address stays out of URLs.
export function useLookupPerson() {
  return useMutation({
    mutationFn: (email: string) => apiFetch<PersonData>("/admin/v1/erasures/lookup", { method: "POST", body: { email } }),
  });
}

export function useErase() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: { email: string; confirm_email: string; reference: string }) =>
      withStepUp(() => apiFetch<{ erasure: Erasure }>("/admin/v1/erasures", { method: "POST", body })).then((d) => d.erasure),
    onSuccess: () => {
      for (const k of ["erasures", "contacts", "contact-stats"]) void qc.invalidateQueries({ queryKey: [k] });
    },
  });
}
