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
  brake_state?: "healthy" | "watch" | "slowed" | "paused";
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
  frozen_at: string | null;
  frozen_reason: string;
};

export type PendingInvite = {
  id: string;
  email: string;
  name: string;
  company_role: CompanyRole;
  brand_roles: BrandRoles;
  expires_at: string;
  awaiting_approval: boolean;
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
  user_agent?: string;
};

export type Meta = { version: string; env: string; debug?: boolean; company_name?: string };

// --- M2: contacts & imports ---

export type Tier = "active" | "warm" | "at_risk" | "sunset";
export type ContactStatus = "pending_confirmation" | "subscribed" | "quarantined" | "unsubscribed";
export type ConsentBasis = "double_opt_in" | "single_opt_in" | "soft_opt_in";

export type Contact = {
  id: string;
  email: string;
  first_name: string;
  last_name: string;
  status: ContactStatus;
  consent_basis: ConsentBasis;
  consent_source: string;
  consented_at: string | null;
  last_click_at: string | null;
  last_engaged_at: string | null;
  tags: string[];
  tracking_allowed: boolean;
  timezone: string;
  country: string;
  created_at: string;
  tier: Tier;
  suppression_reason: string | null;
};

export type ContactStats = {
  total: number;
  tiers: Record<Tier, number>;
  quarantined: number;
  pending_confirmation: number;
  unsubscribed: number;
  suppressed_in_brand: number;
  suppressed_company_wide?: number; // not shown to viewers
};

export type ConsentRecord = {
  id: number;
  kind: "opt_in" | "confirmed" | "import_attested" | "unsubscribed" | "resubscribed";
  method: string;
  source: string;
  wording: string;
  page_url: string;
  occurred_at: string;
};

export type ContactDetail = { contact: Contact; topic_ids: string[]; consent_history: ConsentRecord[] };

export type Topic = { id: string; name: string; subscribers: number };

export type ImportMapping = {
  email: string;
  first_name: string;
  last_name: string;
  tags: string;
  consented_at: string;
  last_engaged_at: string;
  rating: string;
  tracking: string;
  timezone: string;
  country: string;
};

export type TrackingMode = "eu_by_country" | "all" | "none" | "column";

export type ImportConsent = {
  source: string;
  source_detail: string;
  collected_period: string;
  wording: string;
  legal_basis: "consent" | "soft_opt_in" | "";
  double_opt_in: boolean;
  tracking: TrackingMode;
  attested: boolean;
  attested_by_email?: string;
  attested_at?: string;
};

export type Outcome =
  | "ready"
  | "quarantine"
  | "existing"
  | "unsubscribed"
  | "suppressed"
  | "role"
  | "disposable"
  | "invalid"
  | "no_mailbox"
  | "duplicate";

export type CheckResult = {
  total: number;
  counts: Partial<Record<Outcome, number>>;
  samples: Partial<Record<Outcome, string[]>>;
  no_mailbox_domains: string[];
  no_tracking: number;
  dates_mapped: boolean;
  gate: {
    invalid_rate: number;
    invalid_limit: number;
    role_disposable_rate: number;
    role_disposable_limit: number;
    consent_recorded: boolean;
    passed: boolean;
    problems: string[];
  };
  checked_at: string;
};

export type ImportResult = {
  created: number;
  quarantined: number;
  updated: number;
  left_unsubscribed: number;
  left_suppressed: number;
  rejected: number;
  finished_at: string;
};

export type ImportStatus = "uploaded" | "checking" | "checked" | "importing" | "done" | "failed" | "cancelled";

export type Import = {
  id: string;
  filename: string;
  row_count: number;
  headers: string[];
  mapping: ImportMapping;
  consent: Partial<ImportConsent>;
  topic_ids: string[];
  status: ImportStatus;
  check_result: CheckResult | null;
  import_result: ImportResult | null;
  error: string;
  created_at: string;
  updated_at: string;
};

export type ImportView = {
  import: Import;
  preview: string[][];
  attestation_text: string;
  sources: Record<string, string>;
};

// --- M5: governance ---

export type ApprovalStatus = "pending" | "approved" | "rejected" | "cancelled" | "expired";

export type ApprovalItem = {
  id: string;
  kind: string;
  label: string;
  brand_id: string | null;
  brand_name: string;
  brand_slug: string;
  requested_by: string;
  requester_name: string;
  requester_email: string;
  requested_at: string;
  summary: string;
  reason: string;
  status: ApprovalStatus;
  decided_by: string | null;
  decider_name: string;
  decided_at: string | null;
  decision_note: string;
  expires_at: string;
  can_decide: boolean;
  mine: boolean;
  detail: string; // live context for the approver, e.g. how many contacts match now
};

export type SafetyLimit = {
  key: string;
  group: "brakes" | "audience" | "imports";
  label: string;
  unit: string;
  default: number;
  hard_limit: number;
  looser_is_higher: boolean;
  locked: boolean;
  help: string;
  value: number;
  changed_by: string;
  changed_at: string | null;
};

export type LimitChangeResult = { applied: string[]; requested: { id: string; summary: string }[] };

export type ExportItem = {
  id: string;
  brand_id: string;
  brand_name: string;
  approval_id: string;
  row_count: number;
  status: "waiting" | "ready" | "expired" | "rejected" | "cancelled";
  ready_at: string | null;
  expires_at: string | null;
  download_count: number;
  created_at: string;
};

export type Alert = {
  id: string;
  kind: string;
  severity: "info" | "warning" | "critical";
  user_id: string | null;
  brand_id: string | null;
  message: string;
  created_at: string;
  acknowledged_at: string | null;
};
