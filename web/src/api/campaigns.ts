import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "./client";
import { withStepUp } from "./passkeys";

// Shapes and hooks for api/internal/campaigns: campaigns (S10), their
// audience (S5), the confirmation screen (S6), seeds and the brand sender.

export type CampaignState =
  | "draft"
  | "needs_approval"
  | "scheduled"
  | "test_slice"
  | "holding"
  | "sending"
  | "paused"
  | "done"
  | "cancelled";

export type Campaign = {
  id: string;
  name: string;
  topic_id: string;
  template_id: string | null;
  subject: string;
  preheader: string;
  include_at_risk: boolean;
  state: CampaignState;
  paused_from: CampaignState | null;
  pause_reason: string;
  waiting_reason: string;
  version: number;
  from_header: string;
  test_slice_pct: number | null;
  hold_minutes: number | null;
  scheduled_at: string | null;
  start_at: string | null;
  confirmed_at: string | null;
  approval_id: string | null;
  approval_reasons: string[];
  hold_until: string | null;
  audience_count: number;
  excluded_count: number;
  updated_at: string;
  finished_at: string | null;
};

export type ExclusionReason = "suppressed" | "unsubscribed" | "quarantined" | "pending_confirmation" | "sunset" | "at_risk" | "frequency_cap";

export type AudienceSummary = {
  included: number;
  excluded: Partial<Record<ExclusionReason, number>>;
  first_time_imported: number;
  at_risk: number;
};

export type SendProgress = Record<"test" | "rest", Partial<Record<"queued" | "sending" | "sent" | "failed" | "skipped" | "unknown", number>>>;

export type CampaignResponse = {
  campaign: Campaign;
  audience?: AudienceSummary; // drafts: live counts
  breakdown?: Record<string, number>; // scheduled: the stored snapshot
  progress?: SendProgress;
};

export type Review = {
  audience: AudienceSummary;
  approval_reasons: { code: string; text: string }[];
  problems: string[];
  test_slice_pct: number;
  hold_minutes: number;
  from_header: string;
  confirm_text: string;
  cancel_window_minutes: number;
};

export type DraftInput = {
  name: string;
  topic_id: string;
  template_id: string | null;
  subject: string;
  preheader: string;
  include_at_risk: boolean;
  version?: number;
};

export type BrandSender = { from_name: string; from_local: string; reply_to: string };
export type Seed = { email: string; created_at: string };

const brandPath = (slug: string) => `/admin/v1/brands/${slug}`;

export function useCampaigns(slug: string) {
  return useQuery({
    queryKey: ["campaigns", slug],
    queryFn: async () => (await apiFetch<{ campaigns: Campaign[] }>(`${brandPath(slug)}/campaigns`)).campaigns,
  });
}

// useCampaign polls while a campaign is on its way, so progress moves.
export function useCampaign(slug: string, id: string | undefined) {
  return useQuery({
    queryKey: ["campaigns", slug, id],
    queryFn: () => apiFetch<CampaignResponse>(`${brandPath(slug)}/campaigns/${id}`),
    enabled: !!id,
    refetchInterval: (q) => {
      const st = q.state.data?.campaign.state;
      return st && ["scheduled", "test_slice", "holding", "sending"].includes(st) ? 5000 : false;
    },
  });
}

export function useReview(slug: string, id: string, enabled = true) {
  return useQuery({
    queryKey: ["campaigns", slug, id, "review"],
    queryFn: () => apiFetch<Review>(`${brandPath(slug)}/campaigns/${id}/review`),
    enabled,
  });
}

function useCampaignMutation<T>(slug: string, fn: (input: T) => Promise<CampaignResponse>) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSuccess: (d) => {
      qc.setQueryData(["campaigns", slug, d.campaign.id], d);
      void qc.invalidateQueries({ queryKey: ["campaigns", slug], exact: true });
      void qc.invalidateQueries({ queryKey: ["campaigns", slug, d.campaign.id, "review"] });
    },
  });
}

export function useSaveCampaign(slug: string, id: string | undefined) {
  return useCampaignMutation(slug, (d: DraftInput) =>
    id
      ? apiFetch<CampaignResponse>(`${brandPath(slug)}/campaigns/${id}`, { method: "PUT", body: d })
      : apiFetch<CampaignResponse>(`${brandPath(slug)}/campaigns`, { method: "POST", body: d }),
  );
}

export function useSchedule(slug: string, id: string) {
  return useCampaignMutation(slug, (b: { version: number; confirm: string; scheduled_at?: string }) =>
    withStepUp(() => apiFetch<CampaignResponse>(`${brandPath(slug)}/campaigns/${id}/schedule`, { method: "POST", body: b })),
  );
}

export function useCampaignAction(slug: string, id: string, action: "cancel" | "pause" | "resume") {
  return useCampaignMutation(slug, () => {
    const call = () => apiFetch<CampaignResponse>(`${brandPath(slug)}/campaigns/${id}/${action}`, { method: "POST" });
    return action === "resume" ? withStepUp(call) : call();
  });
}

export function useTestSend(slug: string, id: string) {
  return useMutation({
    mutationFn: (emails: string[]) =>
      apiFetch<{ sent: number; to: string[] }>(`${brandPath(slug)}/campaigns/${id}/test`, { method: "POST", body: { emails } }),
  });
}

export function useSeeds(slug: string) {
  return useQuery({
    queryKey: ["seeds", slug],
    queryFn: async () => (await apiFetch<{ seeds: Seed[] }>(`${brandPath(slug)}/seeds`)).seeds,
  });
}

export function useSeedActions(slug: string) {
  const qc = useQueryClient();
  const done = (d: { seeds: Seed[] }) => qc.setQueryData(["seeds", slug], d.seeds);
  return {
    add: useMutation({
      mutationFn: (email: string) =>
        withStepUp(() => apiFetch<{ seeds: Seed[] }>(`${brandPath(slug)}/seeds`, { method: "POST", body: { email } })),
      onSuccess: done,
    }),
    remove: useMutation({
      mutationFn: (email: string) =>
        withStepUp(() => apiFetch<{ seeds: Seed[] }>(`${brandPath(slug)}/seeds/${encodeURIComponent(email)}`, { method: "DELETE" })),
      onSuccess: done,
    }),
  };
}

export function useSender(slug: string) {
  return useQuery({
    queryKey: ["sender", slug],
    queryFn: () => apiFetch<{ sender: BrandSender; from_header: string }>(`${brandPath(slug)}/sender`),
  });
}

export function useSaveSender(slug: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (s: BrandSender) =>
      withStepUp(() => apiFetch<{ sender: BrandSender; from_header: string }>(`${brandPath(slug)}/sender`, { method: "PUT", body: s })),
    onSuccess: (d) => qc.setQueryData(["sender", slug], d),
  });
}
