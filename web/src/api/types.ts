// Shapes returned by the Go API (api/internal/*). Kept by hand until the
// OpenAPI spec lands; field names match the JSON exactly.

export type CompanyRole = "member" | "approver" | "owner";
export type BrandRole = "viewer" | "editor" | "sender" | "brand_admin";
export type BrandRoles = Record<string, BrandRole>; // brand id → role

export type Me = {
  user: { id: string; email: string; name: string; company_role: CompanyRole };
  brand_roles: BrandRoles;
  stepped_up_until: string | null;
};

export type BrandStatus = "setup" | "ready" | "paused";

export type Brand = {
  id: string;
  slug: string;
  name: string;
  color: string;
  postal_address: string;
  sending_domain: string;
  mail_from_domain: string;
  link_domain: string;
  ses_configuration_set: string;
  ses_identity_status: string;
  status: BrandStatus;
  dns_checked_at: string | null;
  created_at: string;
  updated_at: string;
};

export type BrandListItem = Brand & { my_role: BrandRole | "" };

export type DNSRecord = {
  id: string;
  purpose: "dkim" | "mail_from_mx" | "mail_from_spf" | "dmarc";
  type: "CNAME" | "MX" | "TXT";
  name: string;
  value: string;
  status: "pending" | "ok" | "warning" | "failing";
  detail: string;
  checked_at: string | null;
};

export type BrandDetail = { brand: Brand; dns_records: DNSRecord[] };

export type Member = {
  id: string;
  email: string;
  name: string;
  company_role: CompanyRole;
  brand_roles: BrandRoles;
  passkeys: number;
  last_active_at: string | null;
};

export type PendingInvite = {
  id: string;
  email: string;
  name: string;
  company_role: CompanyRole;
  brand_roles: BrandRoles;
  expires_at: string;
};

export type Team = { members: Member[]; invites: PendingInvite[] };

export type AuditEntry = {
  id: number;
  at: string;
  actor_email: string;
  brand_id: string | null;
  action: string;
  target_type: string;
  target_id: string;
  before?: unknown;
  after?: unknown;
  ip: string;
};

export type Meta = { version: string; env: string };
