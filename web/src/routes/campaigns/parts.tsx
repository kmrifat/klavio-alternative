import type { AudienceSummary, CampaignState, ExclusionReason } from "../../api/campaigns";
import { Badge } from "../../components/ui";

export const stateLabel: Record<CampaignState, { label: string; tone: "zinc" | "amber" | "blue" | "indigo" | "green" | "red" }> = {
  draft: { label: "Draft", tone: "zinc" },
  needs_approval: { label: "Waiting for approval", tone: "amber" },
  scheduled: { label: "Scheduled", tone: "blue" },
  test_slice: { label: "Sending test slice", tone: "indigo" },
  holding: { label: "Watching test slice", tone: "indigo" },
  sending: { label: "Sending", tone: "indigo" },
  paused: { label: "Paused", tone: "red" },
  done: { label: "Sent", tone: "green" },
  cancelled: { label: "Cancelled", tone: "zinc" },
};

export function StateBadge({ state }: { state: CampaignState }) {
  const s = stateLabel[state];
  return (
    <Badge tone={s.tone} dot>
      {s.label}
    </Badge>
  );
}

// Why people are left out (S5), in plain words.
export const exclusionLabel: Record<ExclusionReason, string> = {
  suppressed: "On the do-not-email list (bounced, complained or removed)",
  unsubscribed: "Unsubscribed",
  quarantined: "Quarantined import (re-permission first)",
  pending_confirmation: "Hasn't confirmed their signup yet",
  sunset: "No sign of interest for 6+ months",
  at_risk: "Fading interest (3–6 months); include them on purpose",
  frequency_cap: "Emailed too recently",
};

export const fmt = (n: number) => n.toLocaleString("en");

// AudienceBreakdown lists who gets the email and who doesn't, and why.
export function AudienceBreakdown({ included, excluded }: { included: number; excluded: Partial<Record<string, number>> }) {
  const rows = (Object.entries(excluded) as [ExclusionReason, number][]).filter(([, n]) => n > 0).sort((a, b) => b[1] - a[1]);
  const left = rows.reduce((s, [, n]) => s + n, 0);
  return (
    <div className="space-y-3 px-5 py-4">
      <p className="text-sm">
        <span className="text-2xl font-semibold tabular-nums">{fmt(included)}</span>{" "}
        <span className="text-zinc-600">{included === 1 ? "person gets this email" : "people get this email"}</span>
      </p>
      {left > 0 ? (
        <div>
          <p className="text-xs font-medium tracking-wide text-zinc-500 uppercase">{fmt(left)} left out, for their sake and the brand's</p>
          <ul aria-label="Left out" className="mt-1.5 space-y-1 text-sm">
            {rows.map(([reason, n]) => (
              <li key={reason} className="flex justify-between gap-4">
                <span className="text-zinc-700">{exclusionLabel[reason] ?? reason}</span>
                <span className="tabular-nums text-zinc-500">{fmt(n)}</span>
              </li>
            ))}
          </ul>
        </div>
      ) : (
        <p className="text-xs text-zinc-500">Nobody in this topic is left out.</p>
      )}
    </div>
  );
}

export function summaryOf(a: AudienceSummary) {
  return { included: a.included, excluded: a.excluded };
}

// breakdownOf turns the stored snapshot ({included_test, included_rest, <reason>…}) into the same shape.
export function breakdownOf(b: Record<string, number>) {
  const excluded: Record<string, number> = {};
  let included = 0;
  for (const [k, n] of Object.entries(b)) {
    if (k.startsWith("included_")) included += n;
    else excluded[k] = n;
  }
  return { included, excluded };
}
