import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter, RouterProvider } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import { routes } from "./routes";
import { brightside, fakeApi, meta, ownerMe } from "../test/fakeApi";
import type { CheckResult, Contact, ContactStats, Import, ImportView, Me } from "../api/types";

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

const base = "/admin/v1/brands/brightside";
const signedIn = (me: Me = ownerMe, brands = [brightside]) => ({
  "GET /admin/v1/meta": meta,
  "GET /admin/v1/me": { status: 200, body: me },
  "GET /admin/v1/brands": { status: 200, body: { brands } },
  [`GET ${base}/topics`]: { status: 200, body: { topics: [{ id: "t1", name: "Newsletter", subscribers: 3 }] } },
});

const stats: ContactStats = {
  total: 13262,
  tiers: { active: 6120, warm: 2292, at_risk: 1840, sunset: 3010 },
  quarantined: 412,
  pending_confirmation: 0,
  unsubscribed: 0,
  suppressed_in_brand: 2,
  suppressed_company_wide: 4812,
};

function contact(email: string, extra: Partial<Contact> = {}): Contact {
  return {
    id: `c-${email}`, email, first_name: "", last_name: "", status: "subscribed", consent_basis: "double_opt_in",
    consent_source: "via API · newsletter box", consented_at: "2025-03-12T00:00:00Z", last_click_at: null,
    last_engaged_at: null, tags: [], tracking_allowed: true, timezone: "", country: "", created_at: "2025-03-12T00:00:00Z",
    tier: "active", suppression_reason: null, ...extra,
  };
}

describe("contacts (S3)", () => {
  it("shows tier counts, suppression total and contacts with consent proof", async () => {
    const api = fakeApi({
      ...signedIn(),
      [`GET ${base}/contacts/stats`]: { status: 200, body: stats },
      [`GET ${base}/contacts`]: {
        status: 200,
        body: {
          contacts: [
            contact("maya@example.com", { first_name: "Maya", last_name: "Rodriguez" }),
            contact("grace@example.com", { suppression_reason: "hard_bounce" }),
          ],
          next_after: "",
        },
      },
    });
    renderAt("/b/brightside/contacts");
    expect(await screen.findByText("6,120")).toBeInTheDocument();
    expect(screen.getByText("412")).toBeInTheDocument();
    expect(screen.getByText(/4,812 addresses suppressed company-wide/)).toBeInTheDocument();
    const maya = (await screen.findByText("Maya Rodriguez")).closest("tr")!;
    expect(within(maya).getByText("Active · 0–30d")).toBeInTheDocument();
    expect(within(maya).getByText("Double opt-in")).toBeInTheDocument();
    expect(within(screen.getByText("grace@example.com").closest("tr")!).getByText("Suppressed")).toBeInTheDocument();
    // Default filter is Active + Warm.
    const list = api.calls.find((c) => c.path === `${base}/contacts`)!;
    expect(list).toBeDefined();
  });

  it("asks for Active + Warm by default and all tiers on request", async () => {
    const urls: string[] = [];
    fakeApi({
      ...signedIn(),
      [`GET ${base}/contacts/stats`]: { status: 200, body: stats },
      [`GET ${base}/contacts`]: (_b, url) => {
        urls.push(url.search);
        return { status: 200, body: { contacts: [], next_after: "" } };
      },
    });
    renderAt("/b/brightside/contacts");
    await screen.findByText("No contacts match. Import a list or add someone to get started.");
    expect(urls[0]).toContain("tier=active%2Cwarm");
    await userEvent.selectOptions(screen.getByLabelText("Tier"), "all");
    await vi.waitFor(() => expect(urls.at(-1)).not.toContain("tier="));
  });

  it("shows viewers counts only", async () => {
    const viewer: Me = { ...ownerMe, user: { ...ownerMe.user, company_role: "member" }, brand_roles: { "b-brightside": "viewer" } };
    const api = fakeApi({ ...signedIn(viewer, [{ ...brightside, my_role: "viewer" }]), [`GET ${base}/contacts/stats`]: { status: 200, body: stats } });
    renderAt("/b/brightside/contacts");
    expect(await screen.findByText(/Viewers see counts only/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /add contact/i })).toBeNull();
    expect(api.calls.some((c) => c.path === `${base}/contacts`)).toBe(false);
  });

  it("adds a contact with a consent source", async () => {
    let added = false;
    const api = fakeApi({
      ...signedIn(),
      [`GET ${base}/contacts/stats`]: { status: 200, body: stats },
      [`GET ${base}/contacts`]: () => ({ status: 200, body: { contacts: added ? [contact("new@example.com")] : [], next_after: "" } }),
      [`POST ${base}/contacts`]: () => {
        added = true;
        return { status: 201, body: contact("new@example.com") };
      },
    });
    renderAt("/b/brightside/contacts");
    await userEvent.click(await screen.findByRole("button", { name: /add contact/i }));
    const dialog = screen.getByRole("dialog", { name: "Add contact" });
    await userEvent.type(within(dialog).getByLabelText("Email"), "new@example.com");
    await userEvent.type(within(dialog).getByLabelText("Where and how"), "Signed up at the shop");
    await userEvent.click(within(dialog).getByLabelText("Newsletter"));
    await userEvent.click(within(dialog).getByRole("button", { name: "Add contact" }));
    expect(await screen.findByText("new@example.com")).toBeInTheDocument();
    expect(api.calls.find((c) => c.method === "POST")?.body).toMatchObject({
      email: "new@example.com", topic_ids: ["t1"], consent: { basis: "single_opt_in", source: "Signed up at the shop" },
    });
  });
});

describe("import wizard (S4)", () => {
  const checkResult: CheckResult = {
    total: 9680,
    counts: { ready: 8940, quarantine: 412, suppressed: 96, role: 61, disposable: 23, invalid: 148 },
    samples: {},
    no_mailbox_domains: [],
    dates_mapped: true,
    gate: { invalid_rate: 0.015, invalid_limit: 0.02, role_disposable_rate: 0.009, role_disposable_limit: 0.05, consent_recorded: true, passed: true, problems: [] },
    checked_at: "2026-10-10T00:00:00Z",
  };
  const imp: Import = {
    id: "i1", filename: "mailchimp.csv", row_count: 9680, headers: ["Email Address", "First Name", "CONFIRM_TIME", "MEMBER_RATING"],
    mapping: { email: "Email Address", first_name: "First Name", last_name: "", tags: "", consented_at: "CONFIRM_TIME", last_engaged_at: "", rating: "MEMBER_RATING", timezone: "", country: "" },
    consent: {}, topic_ids: [], status: "uploaded", check_result: null, import_result: null, error: "", created_at: "2026-10-10T00:00:00Z",
  };
  const view = (i: Partial<Import>): ImportView => ({
    import: { ...imp, ...i }, preview: [["maya@example.com", "Maya", "2025-03-12", "4"]],
    attestation_text: "I confirm this list was not bought, rented, appended, scraped or shared.",
    sources: { platform_migration: "Moving from another platform", website_form: "Signup form on our website" },
  });

  it("uploads, maps, checks, and imports with a passkey confirmation", async () => {
    let state: Partial<Import> = {};
    let steppedUp = false;
    const api = fakeApi({
      ...signedIn(),
      [`POST ${base}/imports`]: { status: 201, body: view({}) },
      [`GET ${base}/imports/i1`]: () => ({ status: 200, body: view(state) }),
      [`PUT ${base}/imports/i1`]: (body) => {
        state = { ...state, consent: (body as { consent: Import["consent"] }).consent };
        return { status: 200, body: view(state) };
      },
      [`POST ${base}/imports/i1/check`]: () => {
        state = { ...state, status: "checked", check_result: checkResult };
        return { status: 202, body: view(state) };
      },
      [`POST ${base}/imports/i1/commit`]: () => {
        if (!steppedUp) return { status: 403, body: { error: { code: "step_up_required", message: "confirm" } } };
        state = { ...state, status: "done", import_result: { created: 8940, quarantined: 412, updated: 0, left_unsubscribed: 0, left_suppressed: 96, rejected: 232, finished_at: "" } };
        return { status: 202, body: view(state) };
      },
      "POST /admin/v1/auth/stepup/begin": { status: 200, body: { ceremony: "s", options: { publicKey: {} } } },
      "POST /admin/v1/auth/stepup/finish": () => {
        steppedUp = true;
        return { status: 200, body: ownerMe };
      },
      [`GET ${base}/contacts/stats`]: { status: 200, body: stats },
    });
    const router = renderAt("/b/brightside/contacts/import");

    // 1. Upload
    const file = new File(["Email Address\nmaya@example.com\n"], "mailchimp.csv", { type: "text/csv" });
    await userEvent.upload(await screen.findByLabelText("CSV file"), file);

    // 2. Match columns (guessed by the server)
    expect(await screen.findByText(/Match columns · mailchimp.csv/)).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/b/brightside/contacts/import/i1");
    expect(screen.getByLabelText("Column for Engagement rating")).toHaveValue("MEMBER_RATING");
    await userEvent.click(screen.getByRole("button", { name: "Continue" }));

    // 3. Consent + check
    await userEvent.selectOptions(screen.getByLabelText(/^Source/), "platform_migration");
    await userEvent.type(screen.getByLabelText("Which platform?"), "Mailchimp export");
    await userEvent.type(screen.getByLabelText("When were they collected?"), "2023–2026");
    await userEvent.type(screen.getByLabelText("Consent wording or signup page URL"), "brightside.co/subscribe");
    await userEvent.click(screen.getByLabelText(/I confirm this list/));
    await userEvent.click(screen.getByRole("button", { name: "Run safety check" }));
    expect(await screen.findByText("8,940")).toBeInTheDocument();
    expect(screen.getByText(/Passes the import gate/)).toBeInTheDocument();
    const put = api.calls.find((c) => c.method === "PUT")!;
    expect(put.body).toMatchObject({
      topic_ids: ["t1"],
      consent: { source: "platform_migration", source_detail: "Mailchimp export", attested: true },
    });

    // 4. Review + import (server asks for a passkey first)
    await userEvent.click(screen.getByRole("button", { name: "Continue to review" }));
    await userEvent.click(screen.getByRole("button", { name: "Import 9,352 contacts" }));
    expect(await screen.findByText("Import finished")).toBeInTheDocument();
    expect(api.calls.filter((c) => c.path === `${base}/imports/i1/commit`)).toHaveLength(2);
  });

  it("explains why an import doesn't pass", async () => {
    const failed = { ...checkResult, gate: { ...checkResult.gate, passed: false, consent_recorded: false, problems: ["confirm the list wasn't bought, rented or scraped"] } };
    fakeApi({ ...signedIn(), [`GET ${base}/imports/i1`]: { status: 200, body: view({ status: "checked", check_result: failed }) } });
    renderAt("/b/brightside/contacts/import/i1");
    expect(await screen.findByRole("alert")).toHaveTextContent("confirm the list wasn't bought, rented or scraped");
    expect(screen.queryByRole("button", { name: "Continue to review" })).toBeNull();
  });
});
