import { useQuery } from "@tanstack/react-query";
import { apiFetch } from "./client";
import type { SafetyEvent } from "./brakes";
import type { Campaign } from "./campaigns";

// Live monitor (S7) and campaign report (S11). Mirrors api/internal/campaigns/report.go.

export type Rates = { bounce: number; complaint: number; unsubscribe: number }; // fractions

export type Monitor = {
  campaign: Campaign;
  sends: { queued: number; sending: number; sent: number; failed: number; unknown: number; skipped: Record<string, number> };
  feedback: { delivered: number; bounces: number; complaints: number; unsubscribes: number };
  rates: Rates;
  limits: Rates;
  engagement: {
    human_clicks: number;
    unique_clickers: number;
    bot_clicks: number;
    bot_reasons: Record<string, number>;
    human_opens_est: number;
    machine_opens: number;
    soft_bounces: number;
    delivery_delays: number;
  };
  gate: { allowed: boolean; rate_factor: number; needs_approval: boolean; block_inactive: boolean; reason: string } | null;
  safety_events: SafetyEvent[];
  at: string;
};

export type LinkStat = { link_no: number; url: string; human_clicks: number; unique_clickers: number; bot_clicks: number };
export type ProviderStat = { provider: string; sent: number; delivered: number; bounces: number; complaints: number; clickers: number };
export type Report = Monitor & { links: LinkStat[]; providers: ProviderStat[] };

export function useMonitor(slug: string, id: string, live: boolean) {
  return useQuery({
    queryKey: ["campaign-monitor", slug, id],
    queryFn: () => apiFetch<Monitor>(`/admin/v1/brands/${slug}/campaigns/${id}/monitor`),
    refetchInterval: live ? 5_000 : false,
  });
}

export function useReport(slug: string, id: string) {
  return useQuery({
    queryKey: ["campaign-report", slug, id],
    queryFn: () => apiFetch<Report>(`/admin/v1/brands/${slug}/campaigns/${id}/report`),
  });
}

export function pct(f: number, digits = 2) {
  return `${(f * 100).toFixed(digits).replace(/\.?0+$/, "")}%`;
}

export const botReasonLabel: Record<string, string> = {
  honeypot_link: "hidden link",
  head_request: "link check (HEAD)",
  no_user_agent: "no browser",
  scanner_user_agent: "security scanner",
  scanner_ip: "scanner network",
  too_fast_after_delivery: "seconds after delivery",
  many_links_at_once: "every link at once",
};
