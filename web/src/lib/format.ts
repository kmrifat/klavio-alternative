import type { BrandRole, BrandStatus, CompanyRole, ConsentBasis, Contact, DNSRecord, Outcome, Tier } from "../api/types";

export function timeAgo(iso: string | null | undefined, now = Date.now()): string {
  if (!iso) return "never";
  const s = Math.round((now - new Date(iso).getTime()) / 1000);
  if (s < 60) return "now";
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  return `${Math.floor(s / 86400)}d ago`;
}

export function initials(name: string): string {
  return name
    .split(/\s+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((p) => p[0]!.toUpperCase())
    .join("");
}

export const brandRoleLabel: Record<BrandRole, string> = {
  viewer: "Viewer",
  editor: "Editor",
  sender: "Sender",
  brand_admin: "Brand admin",
};

export const brandRoleHelp: Record<BrandRole, string> = {
  viewer: "Reports without personal data",
  editor: "Build campaigns and templates",
  sender: "Schedule sends within limits",
  brand_admin: "Approve, import, manage the domain",
};

export const companyRoleLabel: Record<CompanyRole, string> = {
  member: "—",
  approver: "Approver",
  owner: "Owner",
};

export const brandStatus: Record<BrandStatus, { label: string; tone: "amber" | "green" | "red" }> = {
  setup: { label: "Setup needed", tone: "amber" },
  ready: { label: "Ready to send", tone: "green" },
  paused: { label: "Paused", tone: "red" },
};

export const dnsPurpose: Record<DNSRecord["purpose"], string> = {
  dkim: "DKIM signing",
  mail_from_mx: "MAIL FROM (MX)",
  mail_from_spf: "MAIL FROM (SPF)",
  dmarc: "DMARC",
};

export function slugify(name: string): string {
  return name
    .toLowerCase()
    .normalize("NFKD")
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 32);
}

export const auditLabel: Record<string, string> = {
  "auth.signed_in": "Signed in",
  "user.joined": "Joined with a passkey",
  "user.invited": "Invited",
  "user.roles_changed": "Changed roles of",
  "brand.created": "Created brand",
  "brand.updated": "Updated brand",
};

export const tierInfo: Record<Tier, { label: string; range: string; tone: "green" | "amber" | "red" | "blue"; help: string }> = {
  active: { label: "Active", range: "0–30 days", tone: "green", help: "Gets every campaign" },
  warm: { label: "Warm", range: "31–90 days", tone: "green", help: "Default audience" },
  at_risk: { label: "At-risk", range: "91–180 days", tone: "amber", help: "Opt-in toggle + warning" },
  sunset: { label: "Sunset", range: "181+ days", tone: "red", help: "Never emailed · suppressed after 365 days" },
};

export const consentBasisLabel: Record<ConsentBasis, string> = {
  double_opt_in: "Double opt-in",
  single_opt_in: "Single opt-in",
  soft_opt_in: "Soft opt-in (customer)",
};

// engagementBadge is the S3 "Engagement" column: blocked states win over the tier.
export function engagementBadge(c: Contact): { label: string; tone: "green" | "amber" | "red" | "blue" | "zinc" } {
  if (c.suppression_reason) return { label: "Suppressed", tone: "red" };
  if (c.status === "unsubscribed") return { label: "Unsubscribed", tone: "zinc" };
  if (c.status === "quarantined") return { label: "Quarantined", tone: "blue" };
  if (c.status === "pending_confirmation") return { label: "Awaiting confirmation", tone: "zinc" };
  const t = tierInfo[c.tier];
  return { label: `${t.label} · ${t.range.replace(" days", "d")}`, tone: t.tone };
}

export function shortDate(iso: string | null | undefined): string {
  return iso ? new Date(iso).toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" }) : "—";
}

export const suppressionLabel: Record<string, string> = {
  hard_bounce: "Hard bounce",
  complaint: "Spam complaint",
  manual: "Removed by request",
  erasure: "Data erased",
};

export const outcomeInfo: Record<Outcome, { label: string; badge: string; tone: "green" | "blue" | "zinc" | "amber" | "red" }> = {
  ready: { label: "Ready to import", badge: "Import", tone: "green" },
  existing: { label: "Already contacts", badge: "Update details", tone: "green" },
  quarantine: { label: "No activity in 12+ months", badge: "Quarantine", tone: "blue" },
  suppressed: { label: "Already suppressed (any brand)", badge: "Stay blocked", tone: "zinc" },
  unsubscribed: { label: "Unsubscribed earlier", badge: "Stay unsubscribed", tone: "zinc" },
  role: { label: "Role addresses (info@, sales@)", badge: "Excluded", tone: "amber" },
  disposable: { label: "Disposable domains", badge: "Rejected", tone: "red" },
  invalid: { label: "Not an email address", badge: "Rejected", tone: "red" },
  no_mailbox: { label: "Domain can't receive email", badge: "Rejected", tone: "red" },
  duplicate: { label: "Duplicates in the file", badge: "Skipped", tone: "zinc" },
};
