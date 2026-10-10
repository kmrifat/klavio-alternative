import { vi } from "vitest";
import type { BrandDetail, BrandListItem, Me, Team } from "../api/types";

type Handler = (body: unknown, url: URL) => { status: number; body?: unknown } | Promise<{ status: number; body?: unknown }>;

// fakeApi stubs fetch with "METHOD /path" handlers and records every call.
export function fakeApi(routes: Record<string, Handler | { status: number; body?: unknown }>) {
  const calls: { method: string; path: string; body: unknown }[] = [];
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), "http://localhost");
    const method = init?.method ?? "GET";
    let body: unknown = init?.body;
    if (typeof body === "string") body = JSON.parse(body);
    calls.push({ method, path: url.pathname, body });
    const route = routes[`${method} ${url.pathname}`];
    if (!route) return new Response(JSON.stringify({ error: { code: "not_found", message: "no fake for " + url.pathname } }), { status: 404 });
    const res = typeof route === "function" ? await route(body, url) : route;
    return new Response(res.status === 204 ? null : JSON.stringify(res.body ?? {}), { status: res.status });
  });
  vi.stubGlobal("fetch", fetchMock);
  return { calls };
}

export const ownerMe: Me = {
  user: { id: "u-owner", email: "owner@acme.test", name: "Shafayet H.", company_role: "owner" },
  brand_roles: {},
  stepped_up_until: null,
};

export function brand(slug: string, name: string, color: string, extra: Partial<BrandListItem> = {}): BrandListItem {
  return {
    id: `b-${slug}`,
    slug,
    name,
    color,
    postal_address: "221B Market St",
    sending_domain: `news.${slug}.co`,
    mail_from_domain: `bounce.news.${slug}.co`,
    link_domain: `go.${slug}.co`,
    ses_configuration_set: `brand-${slug}`,
    ses_identity_status: "PENDING",
    status: "setup",
    dns_checked_at: null,
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-01T00:00:00Z",
    my_role: "brand_admin",
    ...extra,
  };
}

export const brightside = brand("brightside", "Brightside", "#f97316");
export const kettle = brand("kettle", "Kettle & Co", "#10b981", { sending_domain: "mail.kettleandco.com" });

export function detail(b: BrandListItem): BrandDetail {
  return {
    brand: b,
    dns_records: [
      { id: "r1", purpose: "dkim", type: "CNAME", name: `t1._domainkey.${b.sending_domain}`, value: "t1.dkim.amazonses.com", status: "ok", detail: "", checked_at: null },
      { id: "r2", purpose: "mail_from_mx", type: "MX", name: b.mail_from_domain, value: "10 feedback-smtp.us-east-1.amazonses.com", status: "failing", detail: "record not found", checked_at: null },
      { id: "r3", purpose: "dmarc", type: "TXT", name: `_dmarc.${b.sending_domain}`, value: "v=DMARC1; p=none", status: "warning", detail: "DMARC is p=none.", checked_at: null },
    ],
  };
}

export const team: Team = {
  members: [
    { id: "u-owner", email: "owner@acme.test", name: "Shafayet H.", company_role: "owner", brand_roles: {}, passkeys: 2, last_active_at: new Date().toISOString() },
    { id: "u-jon", email: "jon@acme.test", name: "Jon Park", company_role: "member", brand_roles: { "b-brightside": "sender" }, passkeys: 1, last_active_at: null },
  ],
  invites: [],
};

export const meta = { status: 200, body: { version: "v-test", env: "development" } };
