import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "./client";
import type { SafetyEvent } from "./brakes";
import type { Alert, Brand, DNSRecord } from "./types";

// Brand health (S2) and the Safety center (S1). Mirrors api/internal/health.

export type CheckStatus = "ok" | "warning" | "failing" | "unknown";
export type HealthLevel = "healthy" | "watch" | "at_risk" | "unknown";

export type HealthCheck = { label: string; status: CheckStatus; detail: string };

export type HealthComponent = {
  key: "authentication" | "reputation" | "list_quality" | "engagement";
  label: string;
  weight: number;
  score: number | null; // null = no data yet
  summary: string;
  checks: HealthCheck[] | null;
};

export type HealthResult = {
  score: number | null;
  level: HealthLevel;
  weakest: string;
  components: HealthComponent[];
};

export type WarmupStatus = {
  day: number;
  days: number;
  cap: number;
  sent: number;
  complete: boolean;
  started_on: string | null;
};

export type BrandCard = {
  slug: string;
  name: string;
  color: string;
  status: Brand["status"];
  score: number | null;
  level: HealthLevel;
  weakest: string;
  components: { key: HealthComponent["key"]; label: string; score: number | null }[];
  computed_at: string | null;
  warmup: WarmupStatus;
};

export type AccountMeters = {
  available: boolean;
  error?: string;
  region: string;
  max_24_hour_send: number;
  max_send_rate: number;
  sent_last_24_hours: number;
  production_access: boolean;
  sending_enabled: boolean;
  enforcement_status: string;
  marketing_sent_today: number;
};

export type Attention = {
  severity: "critical" | "warning" | "info";
  kind: string;
  brand?: string;
  title: string;
  detail: string;
  link?: string;
};

export type Protection = { key: string; label: string; on: boolean; detail: string };

export type SafetyCenterData = {
  brands: BrandCard[];
  account: AccountMeters;
  needs_attention: Attention[];
  protections: Protection[];
};

// Settings → Sending providers: how the app reaches Amazon SES (read-only).
export type ProviderBrand = {
  slug: string;
  name: string;
  color: string;
  status: Brand["status"];
  sending_domain: string;
  mail_from_domain: string;
  ses_configuration_set: string;
  ses_identity_status: string;
};

export type SendingProvider = {
  provider: "amazon_ses";
  mode: "aws" | "fake";
  region: string;
  app_max_send_rate: number;
  event_bus_arn: string;
  events_queue_url: string;
  account: AccountMeters;
  brands: ProviderBrand[];
};

export type BlocklistResult = { zone: string; domain: string; status: "clean" | "listed" | "unknown"; detail: string; checked_at: string };

export type PostmasterDay = { day: string; spam_ratio: number | null; domain_reputation: string; fetched_at: string };

export type BrandHealth = {
  brand: Brand;
  health: HealthResult;
  computed_at: string | null;
  warmup: WarmupStatus;
  dns_records: DNSRecord[];
  blocklists: BlocklistResult[];
  blocklists_on: boolean;
  postmaster: {
    connected: boolean;
    status: "not_connected" | "waiting" | "ok" | "domain_not_found" | "error";
    detail: string;
    history: PostmasterDay[];
  };
  events: Alert[];
  safety_events: SafetyEvent[];
};

export function useSafetyCenter() {
  return useQuery({
    queryKey: ["safety-center"],
    queryFn: () => apiFetch<SafetyCenterData>("/admin/v1/safety-center"),
    refetchInterval: 60_000,
  });
}

export function useSendingProvider(enabled = true) {
  return useQuery({
    queryKey: ["sending-provider"],
    queryFn: () => apiFetch<SendingProvider>("/admin/v1/sending-provider"),
    enabled,
  });
}

export function useBrandHealth(slug: string | undefined) {
  return useQuery({
    queryKey: ["brand-health", slug],
    queryFn: () => apiFetch<BrandHealth>(`/admin/v1/brands/${slug}/health`),
    enabled: !!slug,
  });
}

export function useRecheckHealth(slug: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => apiFetch<BrandHealth>(`/admin/v1/brands/${slug}/health/recheck`, { method: "POST" }),
    onSuccess: (d) => {
      qc.setQueryData(["brand-health", slug], d);
      for (const k of ["safety-center", "brands", "brand"]) void qc.invalidateQueries({ queryKey: [k] });
    },
  });
}

export const levelInfo: Record<HealthLevel, { label: string; tone: "green" | "amber" | "red" | "zinc"; bar: string; text: string }> = {
  healthy: { label: "Healthy", tone: "green", bar: "bg-green-500", text: "text-green-700" },
  watch: { label: "Watch", tone: "amber", bar: "bg-amber-500", text: "text-amber-700" },
  at_risk: { label: "At risk", tone: "red", bar: "bg-red-500", text: "text-red-700" },
  unknown: { label: "No data yet", tone: "zinc", bar: "bg-zinc-300", text: "text-zinc-500" },
};

export function scoreLevel(score: number | null): HealthLevel {
  if (score === null) return "unknown";
  return score >= 80 ? "healthy" : score >= 60 ? "watch" : "at_risk";
}
