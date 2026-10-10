import type { BrandRole, BrandStatus, CompanyRole, DNSRecord } from "../api/types";

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
