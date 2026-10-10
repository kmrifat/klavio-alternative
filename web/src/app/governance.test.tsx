import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter, RouterProvider } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import { routes } from "./routes";
import { brightside, fakeApi, meta, ownerMe, team } from "../test/fakeApi";
import type { ApprovalItem, SafetyLimit } from "../api/types";

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
};

function approval(extra: Partial<ApprovalItem> = {}): ApprovalItem {
  return {
    id: "a1", kind: "safety_limit", label: "Loosen a safety limit", brand_id: null, brand_name: "", brand_slug: "",
    requested_by: "u-maria", requester_name: "Maria Santos", requester_email: "maria@acme.test",
    requested_at: new Date().toISOString(), summary: "Loosen Campaign pause · bounces: 2% → 2.5%", reason: "Black Friday",
    status: "pending", decided_by: null, decider_name: "", decided_at: null, decision_note: "",
    expires_at: new Date(Date.now() + 86_400_000).toISOString(), can_decide: true, mine: false, detail: "", ...extra,
  };
}

describe("approvals (S15)", () => {
  it("approves with a passkey confirmation and rejects with a reason", async () => {
    let steppedUp = false;
    let decided = false;
    const api = fakeApi({
      ...signedIn,
      "GET /admin/v1/approvals": (_b, url) => ({
        status: 200,
        body: { approvals: url.searchParams.get("view") === "decide" && !decided ? [approval(), approval({ id: "a2", summary: "Export 1,204 Brightside contacts", detail: "1210 contacts match these filters now (1204 when requested)" })] : [] },
      }),
      "POST /admin/v1/approvals/a1/approve": () => {
        if (!steppedUp) return { status: 403, body: { error: { code: "step_up_required", message: "confirm" } } };
        decided = true;
        return { status: 200, body: approval({ status: "approved", can_decide: false }) };
      },
      "POST /admin/v1/approvals/a2/reject": { status: 200, body: approval({ id: "a2", status: "rejected" }) },
      "POST /admin/v1/auth/stepup/begin": { status: 200, body: { ceremony: "s", options: { publicKey: {} } } },
      "POST /admin/v1/auth/stepup/finish": () => {
        steppedUp = true;
        return { status: 200, body: ownerMe };
      },
    });
    renderAt("/approvals");
    const card = (await screen.findByText("Loosen Campaign pause · bounces: 2% → 2.5%")).closest("li")!;
    expect(within(card).getByText(/Maria Santos/)).toBeInTheDocument();
    expect(within(card).getByText("“Black Friday”")).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Waiting for you (2)" })).toBeInTheDocument();
    expect(screen.getByText("1210 contacts match these filters now (1204 when requested)")).toBeInTheDocument();

    const other = screen.getByText("Export 1,204 Brightside contacts").closest("li")!;
    await userEvent.click(within(other).getByRole("button", { name: /reject/i }));
    await userEvent.type(within(other).getByLabelText("Why reject?"), "Use a segment instead");
    await userEvent.click(within(other).getAllByRole("button", { name: /^reject$/i }).at(-1)!);

    await userEvent.click(within(card).getByRole("button", { name: /approve/i }));
    await vi.waitFor(() => expect(api.calls.filter((c) => c.path === "/admin/v1/approvals/a1/approve")).toHaveLength(2));
    expect(api.calls.find((c) => c.path === "/admin/v1/approvals/a2/reject")?.body).toEqual({ note: "Use a segment instead" });
  });

  it("lets the requester cancel and download an approved export", async () => {
    const api = fakeApi({
      ...signedIn,
      "GET /admin/v1/approvals": (_b, url) => ({
        status: 200,
        body: { approvals: url.searchParams.get("view") === "mine" ? [approval({ mine: true, can_decide: false })] : [] },
      }),
      "POST /admin/v1/approvals/a1/cancel": { status: 204 },
      "GET /admin/v1/exports": {
        status: 200,
        body: { exports: [{ id: "e1", brand_id: "b-brightside", brand_name: "Brightside", approval_id: "a9", row_count: 1204, status: "ready", ready_at: null, expires_at: null, download_count: 0, created_at: new Date().toISOString() }] },
      },
      "GET /admin/v1/exports/e1/download": { status: 200, body: "csv" },
    });
    vi.stubGlobal("URL", Object.assign(URL, { createObjectURL: vi.fn(() => "blob:x"), revokeObjectURL: vi.fn() }));
    renderAt("/approvals");
    await userEvent.click(await screen.findByRole("tab", { name: "Your requests" }));
    expect(await screen.findByText("Waiting for someone else")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /cancel request/i }));
    await userEvent.click(await screen.findByRole("button", { name: /download/i }));
    await vi.waitFor(() => expect(api.calls.some((c) => c.path === "/admin/v1/exports/e1/download")).toBe(true));
    expect(api.calls.some((c) => c.path === "/admin/v1/approvals/a1/cancel")).toBe(true);
  });
});

function limit(extra: Partial<SafetyLimit>): SafetyLimit {
  return {
    key: "campaign_pause_bounce_pct", group: "brakes", label: "Campaign pause · bounces", unit: "%", default: 2, hard_limit: 3,
    looser_is_higher: true, locked: false, help: "", value: 2, changed_by: "", changed_at: null, ...extra,
  };
}

describe("safety limits (S17)", () => {
  it("marks loosening as needing approval and saves", async () => {
    const api = fakeApi({
      ...signedIn,
      "GET /admin/v1/safety-limits": {
        status: 200,
        body: { limits: [limit({}), limit({ key: "campaign_pause_complaint_pct", label: "Campaign pause · complaints (min 2)", value: 0.08, hard_limit: 0.08, locked: true })] },
      },
      "PUT /admin/v1/safety-limits": { status: 200, body: { applied: [], requested: [{ id: "a1", summary: "x" }] } },
    });
    renderAt("/settings/limits");
    const input = await screen.findByLabelText("Campaign pause · bounces");
    expect(screen.getByText("Locked")).toBeInTheDocument();
    await userEvent.clear(input);
    await userEvent.type(input, "2.5");
    expect(screen.getByText("Needs approval")).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Why loosen?"), "cleaner list");
    await userEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText(/sent for approval/)).toBeInTheDocument();
    expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual({ changes: { campaign_pause_bounce_pct: 2.5 }, reason: "cleaner list" });
  });
});

describe("audit log (S16)", () => {
  it("filters, including from a link", async () => {
    const urls: string[] = [];
    fakeApi({
      ...signedIn,
      "GET /admin/v1/audit": (_b, url) => {
        urls.push(url.search);
        return {
          status: 200,
          body: {
            entries: [{ id: 9, at: new Date().toISOString(), actor_email: "maria@acme.test", brand_id: null, action: "safety_limit.tightened", target_type: "safety_limit", target_id: "test_slice_pct", before: { value: 5 }, after: { value: 8 }, ip: "203.0.113.9" }],
            next_before: 0,
          },
        };
      },
    });
    renderAt("/audit?action=safety_limit.");
    expect(await screen.findByText("Tightened safety limit")).toBeInTheDocument();
    expect(urls[0]).toContain("action=safety_limit.");
    await userEvent.click(screen.getByRole("button", { name: "details" }));
    expect(screen.getByText(/"value": 8/)).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Person"), "maria");
    await userEvent.click(screen.getByRole("button", { name: "Filter" }));
    await vi.waitFor(() => expect(urls.at(-1)).toContain("actor=maria"));
  });
});

describe("team, alerts and exports", () => {
  it("shows a frozen member with unfreeze, and open alerts in the bell", async () => {
    const frozenTeam = { ...team, members: team.members.map((m) => (m.id === "u-jon" ? { ...m, frozen_at: new Date().toISOString(), frozen_reason: "Sign-in from a new country (RU)" } : m)) };
    const api = fakeApi({
      ...signedIn,
      "GET /admin/v1/team": { status: 200, body: frozenTeam },
      "GET /admin/v1/audit": { status: 200, body: { entries: [], next_before: 0 } },
      "GET /admin/v1/alerts": { status: 200, body: { alerts: [{ id: "al1", kind: "new_country", severity: "critical", user_id: "u-jon", brand_id: null, message: "Sign-in for jon@acme.test from a new country (RU)", created_at: new Date().toISOString(), acknowledged_at: null }] } },
      "POST /admin/v1/team/members/u-jon/unfreeze": { status: 204 },
      "POST /admin/v1/alerts/al1/acknowledge": { status: 204 },
    });
    renderAt("/team");
    const row = (await screen.findByText("Jon Park")).closest("tr")!;
    expect(within(row).getByText("Frozen")).toBeInTheDocument();
    await userEvent.click(within(row).getByRole("button", { name: "Unfreeze Jon Park" }));
    await userEvent.click(screen.getByRole("button", { name: "1 open alerts" }));
    const panel = screen.getByRole("dialog", { name: "Alerts" });
    expect(within(panel).getByText(/new country \(RU\)/)).toBeInTheDocument();
    await userEvent.click(within(panel).getByRole("button", { name: "Dismiss" }));
    await vi.waitFor(() => expect(api.calls.some((c) => c.path === "/admin/v1/alerts/al1/acknowledge")).toBe(true));
    expect(api.calls.some((c) => c.path === "/admin/v1/team/members/u-jon/unfreeze")).toBe(true);
  });

  it("requests an export with a reason", async () => {
    const api = fakeApi({
      ...signedIn,
      "GET /admin/v1/brands/brightside/contacts/stats": { status: 200, body: { total: 3, tiers: { active: 1, warm: 1, at_risk: 0, sunset: 1 }, quarantined: 0, pending_confirmation: 0, unsubscribed: 0, suppressed_in_brand: 0 } },
      "GET /admin/v1/brands/brightside/contacts": { status: 200, body: { contacts: [], next_after: "" } },
      "POST /admin/v1/brands/brightside/exports": { status: 202, body: { export: { id: "e1", row_count: 2 }, requester_frozen: false } },
    });
    renderAt("/b/brightside/contacts");
    await userEvent.click(await screen.findByRole("button", { name: /^export$/i }));
    const dialog = screen.getByRole("dialog", { name: "Export contacts" });
    await userEvent.type(within(dialog).getByLabelText("What's it for?"), "CRM sync");
    await userEvent.click(within(dialog).getByRole("button", { name: "Request export" }));
    expect(await within(dialog).findByText(/Sent for approval/)).toBeInTheDocument();
    expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({ filter: { tiers: ["active", "warm"], status: "", tag: "" }, reason: "CRM sync" });
  });
});

describe("admin API reference link", () => {
  it("shows in Settings only when the server runs with DEBUG on", async () => {
    const limits = { "GET /admin/v1/safety-limits": { status: 200, body: { limits: [limit({})] } } };
    fakeApi({ ...signedIn, ...limits });
    renderAt("/settings/limits");
    await screen.findByLabelText("Campaign pause · bounces");
    expect(screen.queryByRole("link", { name: /Admin API \(debug\)/ })).not.toBeInTheDocument();
  });

  it("opens the reference in a new tab", async () => {
    const limits = { "GET /admin/v1/safety-limits": { status: 200, body: { limits: [limit({})] } } };
    fakeApi({ ...signedIn, ...limits, "GET /admin/v1/meta": { status: 200, body: { ...meta.body, debug: true } } });
    renderAt("/settings/limits");
    const link = await screen.findByRole("link", { name: /Admin API \(debug\)/ });
    expect(link.getAttribute("href")).toMatch(/\/admin\/v1\/docs$/);
    expect(link.getAttribute("target")).toBe("_blank");
  });
});
