import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter, RouterProvider } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import { routes } from "./routes";
import { brand, brightside, fakeApi, meta, ownerMe } from "../test/fakeApi";
import type { Segment, SegmentField } from "../api/segments";
import type { ApiKey } from "../api/contactsApi";

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
  "GET /admin/v1/brands/brightside/health": { status: 404, body: { error: { code: "not_found", message: "x" } } },
};

const fields: SegmentField[] = [
  { key: "tier", label: "Engagement tier", kind: "enum", ops: ["is", "is_not", "in", "not_in"], options: ["active", "warm", "at_risk", "sunset"] },
  { key: "country", label: "Country", kind: "country", ops: ["is", "is_not", "in", "not_in", "is_empty"] },
  { key: "last_click_at", label: "Last click", kind: "date", ops: ["within_days", "older_than_days", "before", "after", "is_empty", "is_not_empty"] },
];

const counts = (matching: number, mailable: number) => ({ matching, mailable, tiers: { active: mailable, warm: 0, at_risk: 0, sunset: 0 } });

const segment: Segment = {
  id: "s1",
  name: "Engaged in Germany",
  rules: { match: "all", groups: [{ match: "all", conditions: [{ field: "country", op: "is", value: "DE" }] }] },
  created_at: "2026-10-01T00:00:00Z",
  updated_at: new Date().toISOString(),
  counts: counts(1204, 980),
  counted_at: new Date().toISOString(),
};

describe("segments (S12)", () => {
  it("lists segments with their counts", async () => {
    fakeApi({ ...signedIn, "GET /admin/v1/brands/brightside/segments": { status: 200, body: { segments: [segment] } } });
    renderAt("/b/brightside/segments");
    const row = (await screen.findByRole("link", { name: "Engaged in Germany" })).closest("tr")!;
    expect(within(row).getByText("1,204")).toBeInTheDocument();
    expect(within(row).getByText("980")).toBeInTheDocument();
  });

  it("builds rules with a live count and saves", async () => {
    let saved: unknown;
    const api = fakeApi({
      ...signedIn,
      "GET /admin/v1/segments/fields": { status: 200, body: { fields } },
      "GET /admin/v1/brands/brightside/topics": { status: 200, body: { topics: [] } },
      "POST /admin/v1/brands/brightside/segments/preview": (body) => {
        const n = JSON.stringify(body).toLowerCase().includes('"de"') ? 42 : 300;
        return { status: 200, body: { counts: counts(n, n - 2), sample: [] } };
      },
      "POST /admin/v1/brands/brightside/segments": (body) => {
        saved = body;
        return { status: 201, body: { ...segment, id: "s9" } };
      },
      "GET /admin/v1/brands/brightside/segments/s9": { status: 200, body: { ...segment, id: "s9" } },
    });
    const router = renderAt("/b/brightside/segments/new");
    // Default rule: tier is Active or Warm.
    expect(await screen.findByLabelText("Matching contacts")).toHaveTextContent("300");
    expect(screen.getByLabelText("Group 1 condition 1 value active")).toBeChecked();

    await userEvent.click(screen.getByRole("button", { name: "Add condition" }));
    await userEvent.type(screen.getByLabelText("Group 1 condition 2 value"), "de");
    await waitFor(() => expect(screen.getByLabelText("Matching contacts")).toHaveTextContent("42"), { timeout: 3000 });
    expect(screen.getByLabelText("Can be emailed")).toHaveTextContent("40");

    await userEvent.type(screen.getByLabelText("Segment name"), "Engaged in Germany");
    await userEvent.click(screen.getByRole("button", { name: "Save segment" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/b/brightside/segments/s9"));
    expect(saved).toEqual({
      name: "Engaged in Germany",
      rules: {
        match: "all",
        groups: [{ match: "all", conditions: [{ field: "tier", op: "in", value: ["active", "warm"] }, { field: "country", op: "is", value: "de" }] }],
      },
    });
    expect(api.calls.some((c) => c.path.endsWith("/segments/preview"))).toBe(true);
  });

  it("shows viewers a saved segment's numbers without live counting", async () => {
    const viewer = { ...ownerMe, user: { ...ownerMe.user, company_role: "member" as const }, brand_roles: { "b-brightside": "viewer" as const } };
    const api = fakeApi({
      ...signedIn,
      "GET /admin/v1/me": { status: 200, body: viewer },
      "GET /admin/v1/brands": { status: 200, body: { brands: [brand("brightside", "Brightside", "#f97316", { my_role: "viewer" })] } },
      "GET /admin/v1/segments/fields": { status: 200, body: { fields } },
      "GET /admin/v1/brands/brightside/topics": { status: 200, body: { topics: [] } },
      "GET /admin/v1/brands/brightside/segments/s1": { status: 200, body: segment },
    });
    renderAt("/b/brightside/segments/s1");
    expect(await screen.findByLabelText("Matching contacts")).toHaveTextContent("1,204");
    expect(screen.getByLabelText("Segment name")).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Save segment" })).toBeNull();
    expect(api.calls.some((c) => c.path.endsWith("/preview"))).toBe(false);
  });

  it("shows the server's rule error in the live count", async () => {
    fakeApi({
      ...signedIn,
      "GET /admin/v1/segments/fields": { status: 200, body: { fields } },
      "GET /admin/v1/brands/brightside/topics": { status: 200, body: { topics: [] } },
      "POST /admin/v1/brands/brightside/segments/preview": {
        status: 400,
        body: { error: { code: "invalid_rules", message: "group 1, condition 1: pick between 1 and 50 values" } },
      },
    });
    renderAt("/b/brightside/segments/new");
    expect(await screen.findByText(/pick between 1 and 50 values/)).toBeInTheDocument();
  });
});

function apiKey(extra: Partial<ApiKey> = {}): ApiKey {
  return {
    id: "k1", name: "Website", prefix: "kla_abcd1234", scopes: ["contacts:write"], ip_allowlist: [],
    created_at: "2026-10-01T00:00:00Z", expires_at: "2026-12-30T00:00:00Z", last_used_at: null, revoked_at: null, state: "active", ...extra,
  };
}

const contactsApiRoutes = {
  "GET /admin/v1/brands/brightside/api-keys": { status: 200, body: { keys: [apiKey()], scopes: ["contacts:read", "contacts:write"] } },
  "GET /admin/v1/brands/brightside/consent-wordings": {
    status: 200,
    body: { wordings: [{ id: "w1", label: "Footer form", text: "Yes, send me Brightside news.", created_at: "2026-10-01T00:00:00Z", retired_at: null }] },
  },
  "GET /admin/v1/brands/brightside/repermission": { status: 200, body: { on: false, sending_ready: true, quarantined: 312, sent_today: 0, per_day: 200 } },
  "GET /admin/v1/brands/brightside/api-calls": {
    status: 200,
    body: { calls: [{ id: 1, key_name: "Website", at: new Date().toISOString(), method: "POST", path: "/api/v1/contacts", status: 202, ip: "203.0.113.9", email_mask: "a•••@example.com", error_code: "" }] },
  },
};

describe("contacts API (S14)", () => {
  it("creates a key, shows the secret once, and manages wordings and re-permission", async () => {
    let keyBody: unknown;
    let rpOn = false;
    fakeApi({
      ...signedIn,
      ...contactsApiRoutes,
      "POST /admin/v1/brands/brightside/api-keys": (body) => {
        keyBody = body;
        return { status: 201, body: { key: apiKey({ id: "k2", name: "App", state: "pending_approval", approval: { id: "a1", status: "pending" } }), secret: "kla_abcd1234_secretsecret" } };
      },
      "PUT /admin/v1/brands/brightside/repermission": (body) => {
        rpOn = (body as { on: boolean }).on;
        return { status: 200, body: { on: rpOn, sending_ready: true, quarantined: 312, sent_today: 0, per_day: 200 } };
      },
    });
    renderAt("/b/brightside/api");
    expect(await screen.findByText("kla_abcd1234_…")).toBeInTheDocument();
    expect(screen.getByText("a•••@example.com")).toBeInTheDocument();
    expect(screen.getByText("“Yes, send me Brightside news.”")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Create key" }));
    const dialog = screen.getByRole("dialog", { name: "Create API key" });
    await userEvent.type(within(dialog).getByLabelText("Name"), "App");
    await userEvent.type(within(dialog).getByLabelText("Allowed IP addresses"), "203.0.113.7, 198.51.100.0/24");
    await userEvent.click(within(dialog).getByRole("button", { name: /create key/i }));
    expect(await screen.findByLabelText("New API key")).toHaveTextContent("kla_abcd1234_secretsecret");
    expect(screen.getByText(/starts working once another admin approves/)).toBeInTheDocument();
    expect(keyBody).toEqual({ name: "App", scopes: ["contacts:write"], ip_allowlist: ["203.0.113.7", "198.51.100.0/24"], expires_in_days: 90 });

    await userEvent.click(screen.getByRole("button", { name: "Done" }));
    await userEvent.click(screen.getByRole("button", { name: "Turn on" }));
    expect(await screen.findByRole("button", { name: "Turn off" })).toBeInTheDocument();
    expect(rpOn).toBe(true);
  });

  it("can't turn on re-permission before email sending is set up", async () => {
    fakeApi({
      ...signedIn,
      ...contactsApiRoutes,
      "GET /admin/v1/brands/brightside/repermission": { status: 200, body: { on: false, sending_ready: false, quarantined: 312, sent_today: 0, per_day: 200 } },
    });
    renderAt("/b/brightside/api");
    expect(await screen.findByRole("button", { name: "Turn on" })).toBeDisabled();
    expect(screen.getByText(/sending isn't set up yet/)).toBeInTheDocument();
  });

  it("shows editors the wordings and docs, but not keys or calls", async () => {
    const editor = { ...ownerMe, user: { ...ownerMe.user, company_role: "member" as const }, brand_roles: { "b-brightside": "editor" as const } };
    const api = fakeApi({
      ...signedIn,
      ...contactsApiRoutes,
      "GET /admin/v1/me": { status: 200, body: editor },
      "GET /admin/v1/brands": { status: 200, body: { brands: [brand("brightside", "Brightside", "#f97316", { my_role: "editor" })] } },
    });
    renderAt("/b/brightside/api");
    expect(await screen.findByText("“Yes, send me Brightside news.”")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Create key" })).toBeNull();
    expect(screen.queryByRole("button", { name: /retire/i })).toBeNull();
    expect(api.calls.some((c) => c.path.endsWith("/api-keys") || c.path.endsWith("/api-calls"))).toBe(false);
  });
});
