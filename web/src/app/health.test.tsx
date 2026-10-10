import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter, RouterProvider } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import { routes } from "./routes";
import { brightside, fakeApi, meta, ownerMe } from "../test/fakeApi";
import type { BrandHealth, HealthResult, SafetyCenterData } from "../api/health";

vi.mock("@simplewebauthn/browser", () => ({
  startAuthentication: vi.fn(async () => ({ id: "cred" })),
  startRegistration: vi.fn(async () => ({ id: "cred" })),
}));

function renderAt(path: string) {
  const router = createMemoryRouter(routes, { initialEntries: [path] });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return router;
}

afterEach(() => vi.unstubAllGlobals());

const signedIn = {
  "GET /admin/v1/meta": meta,
  "GET /admin/v1/me": { status: 200, body: ownerMe },
  "GET /admin/v1/brands": { status: 200, body: { brands: [brightside] } },
  "GET /admin/v1/alerts": { status: 200, body: { alerts: [] } },
  "GET /admin/v1/approvals": { status: 200, body: { approvals: [] } },
};

const warmup = { day: 9, days: 21, cap: 3200, sent: 1200, complete: false, started_on: "2026-10-02" };

const safety: SafetyCenterData = {
  brands: [
    {
      slug: "brightside", name: "Brightside", color: "#f97316", status: "ready", score: 72, level: "watch",
      weakest: "Engagement: Many contacts haven't engaged in months; send them less.",
      components: [
        { key: "authentication", label: "Authentication", score: 94 },
        { key: "reputation", label: "Reputation", score: 100 },
        { key: "list_quality", label: "List quality", score: 81 },
        { key: "engagement", label: "Engagement", score: 41 },
      ],
      computed_at: new Date().toISOString(),
      warmup,
    },
  ],
  account: {
    available: true, region: "us-east-1", max_24_hour_send: 50000, max_send_rate: 14, sent_last_24_hours: 12000,
    production_access: true, sending_enabled: true, enforcement_status: "HEALTHY", marketing_sent_today: 1200,
  },
  needs_attention: [
    { severity: "critical", kind: "blocklisted", brand: "brightside", title: "Brightside is on a blocklist", detail: "Listed: brightside.co on Spamhaus DBL", link: "/b/brightside/health" },
    { severity: "info", kind: "postmaster_not_connected", title: "Gmail Postmaster Tools isn't connected", detail: "Set POSTMASTER_CREDENTIALS_FILE." },
  ],
  protections: [
    { key: "two_person", label: "Two-person approvals", on: true, detail: "Risky actions need a second admin." },
    { key: "postmaster", label: "Gmail Postmaster Tools", on: false, detail: "Not connected." },
  ],
};

function health(score: number | null, extra: Partial<HealthResult> = {}): HealthResult {
  return {
    score,
    level: score === null ? "unknown" : score >= 80 ? "healthy" : score >= 60 ? "watch" : "at_risk",
    weakest: "",
    components: [
      {
        key: "authentication", label: "Authentication", weight: 0.25, score: 94, summary: "Works, but DMARC is still on p=none.",
        checks: [
          { label: "DKIM", status: "ok", detail: "Signs every email" },
          { label: "DMARC", status: "warning", detail: "Policy is p=none." },
        ],
      },
      {
        key: "reputation", label: "Reputation", weight: 0.35, score: 0, summary: "On a blocklist: mail is likely going to spam.",
        checks: [{ label: "Blocklists", status: "failing", detail: "Listed: brightside.co on Spamhaus DBL" }],
      },
      { key: "list_quality", label: "List quality", weight: 0.2, score: null, summary: "No contacts yet.", checks: null },
      { key: "engagement", label: "Engagement", weight: 0.2, score: null, summary: "No one to email yet.", checks: null },
    ],
    ...extra,
  };
}

function brandHealth(extra: Partial<BrandHealth> = {}): BrandHealth {
  return {
    brand: { ...brightside, status: "ready" },
    health: health(55, { weakest: "Reputation: On a blocklist: mail is likely going to spam." }),
    computed_at: new Date().toISOString(),
    warmup,
    dns_records: [],
    blocklists: [
      { zone: "Spamhaus DBL", domain: "brightside.co", status: "listed", detail: "spam domain", checked_at: new Date().toISOString() },
      { zone: "SURBL", domain: "brightside.co", status: "clean", detail: "", checked_at: new Date().toISOString() },
    ],
    blocklists_on: true,
    postmaster: { connected: false, status: "not_connected", detail: "", history: [] },
    events: [],
    ...extra,
  };
}

describe("safety center (S1)", () => {
  it("shows what needs attention, brand cards, the shared account and protections", async () => {
    fakeApi({ ...signedIn, "GET /admin/v1/safety-center": { status: 200, body: safety } });
    renderAt("/");
    const attention = await screen.findByRole("list", { name: "Needs attention" });
    const items = within(attention).getAllByRole("listitem");
    expect(items[0]).toHaveTextContent("Brightside is on a blocklist");
    expect(within(items[0]!).getByRole("link")).toHaveAttribute("href", "/b/brightside/health");
    expect(items[1]).toHaveTextContent("Gmail Postmaster Tools isn't connected");

    const card = screen.getByRole("link", { name: "Brightside health" });
    expect(card).toHaveAttribute("href", "/b/brightside/health");
    expect(within(card).getByText("72")).toBeInTheDocument();
    expect(within(card).getByText(/Watch/)).toBeInTheDocument();
    expect(within(card).getByText("Warm-up day 9 of ~21 · 1,200 / 3,200 today", { exact: false })).toBeInTheDocument();

    expect(screen.getByRole("meter", { name: "Sent in the last 24 hours" })).toHaveAttribute("aria-valuenow", "24");
    expect(screen.getByText("Production access")).toBeInTheDocument();
    expect(screen.getByText("Two-person approvals").closest("li")).toHaveTextContent("On");
    expect(screen.getByText("Gmail Postmaster Tools", { selector: "p" }).closest("li")).toHaveTextContent("Off");
  });

  it("says when nothing needs attention", async () => {
    fakeApi({ ...signedIn, "GET /admin/v1/safety-center": { status: 200, body: { ...safety, needs_attention: [] } } });
    renderAt("/safety");
    expect(await screen.findByText("Nothing needs your attention right now.")).toBeInTheDocument();
  });

  it("warns when the SES account can't be read or is in the sandbox", async () => {
    fakeApi({
      ...signedIn,
      "GET /admin/v1/safety-center": { status: 200, body: { ...safety, account: { ...safety.account, production_access: false } } },
    });
    renderAt("/safety");
    expect(await screen.findByText("Sandbox")).toBeInTheDocument();
  });
});

describe("brand members", () => {
  it("start at their brand's health and don't see the Safety center", async () => {
    const member = { ...ownerMe, user: { ...ownerMe.user, company_role: "member" as const }, brand_roles: { "b-brightside": "viewer" as const } };
    fakeApi({
      ...signedIn,
      "GET /admin/v1/me": { status: 200, body: member },
      "GET /admin/v1/brands/brightside/health": { status: 200, body: brandHealth() },
    });
    const router = renderAt("/");
    expect(await screen.findByRole("heading", { name: "Brand health" })).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/b/brightside/health");
    expect(within(screen.getByRole("navigation", { name: "Company" })).queryByText("Safety center")).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: /Sending as/ }));
    expect(screen.queryByRole("menuitemradio", { name: /company view/i })).toBeNull();
  });
});

describe("brand health (S2)", () => {
  it("shows the score, its parts, warm-up and blocklists, and re-checks", async () => {
    let rechecked = false;
    const api = fakeApi({
      ...signedIn,
      "GET /admin/v1/brands/brightside/health": { status: 200, body: brandHealth() },
      "POST /admin/v1/brands/brightside/health/recheck": () => {
        rechecked = true;
        return {
          status: 200,
          body: brandHealth({
            health: health(96),
            blocklists: [{ zone: "Spamhaus DBL", domain: "brightside.co", status: "clean", detail: "", checked_at: new Date().toISOString() }],
          }),
        };
      },
      "GET /admin/v1/safety-center": { status: 200, body: safety },
    });
    renderAt("/b/brightside/health");
    const score = await screen.findByRole("region", { name: "Health score" });
    expect(within(score).getByText("55")).toBeInTheDocument();
    expect(within(score).getByText("At risk")).toBeInTheDocument();
    expect(within(score).getByText(/On a blocklist/)).toBeInTheDocument();

    expect(screen.getByLabelText("Reputation score")).toHaveTextContent("0");
    expect(screen.getByLabelText("List quality score")).toHaveTextContent("—");
    expect(screen.getByText("Day 9 of ~21")).toBeInTheDocument();
    expect(screen.getByText("Listed on 1 list(s). Mail is likely going to spam.")).toBeInTheDocument();
    expect(screen.getByText(/Not connected\. An owner can connect it/)).toBeInTheDocument();
    // The top bar shows the brand's health instead of the API status.
    expect(screen.getByRole("link", { name: "Brand health: 55" })).toHaveAttribute("href", "/b/brightside/health");

    await userEvent.click(screen.getByRole("button", { name: "Re-check now" }));
    expect(await within(score).findByText("96")).toBeInTheDocument();
    expect(rechecked).toBe(true);
    expect(screen.getByText("Not listed anywhere.")).toBeInTheDocument();
    expect(api.calls.filter((c) => c.method === "POST")).toHaveLength(1);
  });

  it("explains Postmaster states and blocklists being off", async () => {
    fakeApi({
      ...signedIn,
      "GET /admin/v1/brands/brightside/health": {
        status: 200,
        body: brandHealth({
          blocklists: [],
          blocklists_on: false,
          postmaster: {
            connected: true, status: "ok", detail: "",
            history: [{ day: "2026-10-08T00:00:00Z", spam_ratio: 0.0021, domain_reputation: "LOW", fetched_at: "2026-10-09T00:00:00Z" }],
          },
          warmup: { ...warmup, complete: true },
        }),
      },
    });
    renderAt("/b/brightside/health");
    expect(await screen.findByText(/Blocklist checks are off/)).toBeInTheDocument();
    expect(screen.getByText("0.21%")).toBeInTheDocument();
    expect(screen.getByText("LOW")).toBeInTheDocument();
    expect(screen.getByText("Complete")).toBeInTheDocument();
  });
});
