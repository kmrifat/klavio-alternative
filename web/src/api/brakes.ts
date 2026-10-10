import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "./client";
import { withStepUp } from "./passkeys";
import type { Brand } from "./types";

// Brakes and recovery (§6.4, S8). Mirrors api/internal/brakes.

export type BrakeState = "healthy" | "watch" | "slowed" | "paused";

export type SafetyEvent = {
  id: number;
  at: string;
  scope: "campaign" | "brand" | "account";
  brand_id: string | null;
  campaign_id: string | null;
  action: "watch" | "slowed" | "paused" | "cleared" | "stopped";
  from_state: string;
  to_state: string;
  reason: string;
};

export type RecoveryStep = { id: string; title: string; help: string };

export type Recovery = {
  brand: Brand & { brake_state: BrakeState };
  paused: boolean;
  braked: boolean; // any brake on: paused, slowed or watch
  current: BrakeState;
  next: BrakeState; // what an approved request loosens it to
  event: SafetyEvent | null;
  counts: { delivered: number; bounces: number; complaints: number; unsubscribes: number };
  still_over: boolean;
  why: string;
  steps: RecoveryStep[];
  pending: { id: string; requested_at: string; summary: string } | null;
  events: SafetyEvent[];
};

export const brakeInfo: Record<BrakeState, { label: string; tone: "green" | "amber" | "red" | "zinc"; help: string }> = {
  healthy: { label: "Brake: off", tone: "green", help: "Sending normally." },
  watch: { label: "Brake: watch", tone: "amber", help: "Inactive contacts are left out until the rates settle." },
  slowed: { label: "Brake: slowed", tone: "amber", help: "Half the send rate, and every send needs a second admin." },
  paused: { label: "Paused", tone: "red", help: "All marketing email is stopped until recovery." },
};

export function useRecovery(slug: string) {
  return useQuery({
    queryKey: ["recovery", slug],
    queryFn: () => apiFetch<Recovery>(`/admin/v1/brands/${slug}/recovery`),
  });
}

export function useRequestResume(slug: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: { steps: string[]; reason: string }) =>
      withStepUp(() => apiFetch<{ approval: { id: string } }>(`/admin/v1/brands/${slug}/recovery/resume`, { method: "POST", body })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["recovery", slug] });
      void qc.invalidateQueries({ queryKey: ["approvals"] });
    },
  });
}

export type AccountBrake = { stopped_at: string | null; reason: string; pending: { id: string } | null };

export function useAccountBrake() {
  return useQuery({ queryKey: ["account-brake"], queryFn: () => apiFetch<AccountBrake>("/admin/v1/account-brake") });
}

export function useRequestAccountRelease() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: { steps: string[]; reason: string }) =>
      withStepUp(() => apiFetch<{ approval: { id: string } }>("/admin/v1/account-brake/release", { method: "POST", body })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["account-brake"] });
      void qc.invalidateQueries({ queryKey: ["approvals"] });
    },
  });
}
