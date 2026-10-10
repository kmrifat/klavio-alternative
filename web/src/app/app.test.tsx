import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter, RouterProvider } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import { routes } from "./routes";
import { brightside, detail, fakeApi, kettle, meta, ownerMe, team } from "../test/fakeApi";
import type { Me } from "../api/types";

vi.mock("@simplewebauthn/browser", () => ({
  startAuthentication: vi.fn(async () => ({ id: "cred", type: "public-key" })),
  startRegistration: vi.fn(async () => ({ id: "cred", type: "public-key" })),
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

const signedIn = (me: Me = ownerMe, brands = [brightside, kettle]) => ({
  "GET /admin/v1/meta": meta,
  "GET /admin/v1/me": { status: 200, body: me },
  "GET /admin/v1/brands": { status: 200, body: { brands } },
});

afterEach(() => vi.unstubAllGlobals());

describe("auth", () => {
  it("sends signed-out visitors to /login and signs in with a passkey", async () => {
    let signedInNow = false;
    const api = fakeApi({
      "GET /admin/v1/meta": meta,
      "GET /admin/v1/me": () => (signedInNow ? { status: 200, body: ownerMe } : { status: 401, body: { error: { code: "unauthenticated", message: "sign in" } } }),
      "GET /admin/v1/brands": { status: 200, body: { brands: [brightside] } },
      "POST /admin/v1/auth/login/begin": { status: 200, body: { ceremony: "c1", options: { publicKey: { challenge: "abc" } } } },
      "POST /admin/v1/auth/login/finish": () => {
        signedInNow = true;
        return { status: 200, body: ownerMe };
      },
    });
    const router = renderAt("/team");
    await userEvent.click(await screen.findByRole("button", { name: /sign in with passkey/i }));
    expect(await screen.findByRole("heading", { name: "Team & access" })).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/team");
    expect(api.calls.find((c) => c.path === "/admin/v1/auth/login/finish")?.body).toEqual({ id: "cred", type: "public-key" });
  });

  it("ignores ?next= pointing at another site", async () => {
    fakeApi(signedIn());
    const router = renderAt("/login?next=//evil.example/x");
    await screen.findByRole("heading", { name: "Safety center" });
    expect(router.state.location.pathname).toBe("/safety");
  });

  it("accepts an invite by creating a passkey", async () => {
    let joined = false;
    fakeApi({
      "GET /admin/v1/meta": meta,
      "GET /admin/v1/me": () => (joined ? { status: 200, body: ownerMe } : { status: 401 }),
      "GET /admin/v1/brands": { status: 200, body: { brands: [] } },
      "GET /admin/v1/invites/tok": { status: 200, body: { email: "maria@acme.test", name: "Maria", expires_at: "2026-10-17T00:00:00Z" } },
      "POST /admin/v1/invites/tok/begin": { status: 200, body: { ceremony: "c1", options: { publicKey: {} } } },
      "POST /admin/v1/invites/tok/finish": () => {
        joined = true;
        return { status: 200, body: ownerMe };
      },
    });
    renderAt("/invite/tok");
    expect(await screen.findByText("maria@acme.test")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /create passkey/i }));
    expect(await screen.findByRole("heading", { name: "Safety center" })).toBeInTheDocument();
  });

  it("explains an invalid invite", async () => {
    fakeApi({
      "GET /admin/v1/invites/bad": { status: 404, body: { error: { code: "invite_invalid", message: "this invite link is invalid, used or expired" } } },
    });
    renderAt("/invite/bad");
    expect(await screen.findByText(/invalid, used or expired/)).toBeInTheDocument();
  });
});

describe("app shell", () => {
  it("shows company view with real brands and the signed-in user", async () => {
    fakeApi(signedIn());
    renderAt("/");
    expect(await screen.findByRole("heading", { name: "Safety center" })).toBeInTheDocument();
    expect(screen.getByText("Company view")).toBeInTheDocument();
    expect(screen.getByText("Shafayet H.")).toBeInTheDocument();
    expect(screen.getByText("Owner · all brands")).toBeInTheDocument();
    expect(await screen.findByText("API v-test")).toBeInTheDocument();
  });

  it("shows the brand as 'Sending as' on brand pages and remembers it", async () => {
    fakeApi(signedIn());
    renderAt("/b/kettle/contacts");
    expect(await screen.findByText("mail.kettleandco.com")).toBeInTheDocument();
    expect(screen.getByText("Sending as")).toBeInTheDocument();
    await userEvent.click(within(screen.getByRole("navigation", { name: "Company" })).getByText("Audit log"));
    const brandNav = await screen.findByRole("navigation", { name: "Brand" });
    expect(within(brandNav).getByText("Templates").closest("a")).toHaveAttribute("href", "/b/kettle/templates");
  });

  it("shows not found for a brand the user can't see", async () => {
    fakeApi(signedIn());
    renderAt("/b/nimbus/campaigns");
    expect(await screen.findByRole("heading", { name: "Page not found" })).toBeInTheDocument();
  });

  it("guides an owner with no brands to add one", async () => {
    fakeApi(signedIn(ownerMe, []));
    renderAt("/safety");
    expect(await screen.findByRole("link", { name: /add your first brand/i })).toHaveAttribute("href", "/settings/brands/new");
    const brandNav = screen.getByRole("navigation", { name: "Brand" });
    expect(within(brandNav).getByText("Campaigns").closest("a")).toBeNull();
  });

  it("signs out", async () => {
    let out = false;
    fakeApi({
      ...signedIn(),
      "GET /admin/v1/me": () => (out ? { status: 401 } : { status: 200, body: ownerMe }),
      "POST /admin/v1/auth/logout": () => {
        out = true;
        return { status: 204 };
      },
    });
    const router = renderAt("/safety");
    await userEvent.click(await screen.findByRole("button", { name: "Sign out" }));
    expect(await screen.findByRole("button", { name: /sign in with passkey/i })).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/login");
  });
});

describe("team & access", () => {
  it("lists members with their role per brand", async () => {
    fakeApi({ ...signedIn(), "GET /admin/v1/team": { status: 200, body: team }, "GET /admin/v1/audit": { status: 200, body: { entries: [] } } });
    renderAt("/team");
    const row = (await screen.findByText("Jon Park")).closest("tr")!;
    expect(within(row).getByText("Sender")).toBeInTheDocument();
    expect(within(row).getByText("No access")).toBeInTheDocument();
    expect(screen.getByText("(you)")).toBeInTheDocument();
  });

  it("hides owner controls from members", async () => {
    const member: Me = { ...ownerMe, user: { ...ownerMe.user, company_role: "member" }, brand_roles: { "b-brightside": "sender" } };
    fakeApi({ ...signedIn(member, [{ ...brightside, my_role: "sender" }]), "GET /admin/v1/team": { status: 200, body: team } });
    renderAt("/team");
    await screen.findByText("Jon Park");
    expect(screen.queryByRole("button", { name: /invite member/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /edit roles/i })).toBeNull();
    expect(screen.queryByText("Audit log", { selector: "h2" })).toBeNull();
  });

  it("invites a member, confirming with a passkey when asked", async () => {
    let steppedUp = false;
    const api = fakeApi({
      ...signedIn(),
      "GET /admin/v1/team": { status: 200, body: team },
      "GET /admin/v1/audit": { status: 200, body: { entries: [] } },
      "POST /admin/v1/team/invites": () =>
        steppedUp
          ? { status: 201, body: { invite_url: "http://localhost:5173/invite/xyz", expires_at: "2026-10-17T00:00:00Z" } }
          : { status: 403, body: { error: { code: "step_up_required", message: "confirm" } } },
      "POST /admin/v1/auth/stepup/begin": { status: 200, body: { ceremony: "s1", options: { publicKey: {} } } },
      "POST /admin/v1/auth/stepup/finish": () => {
        steppedUp = true;
        return { status: 200, body: ownerMe };
      },
    });
    renderAt("/team");
    await userEvent.click(await screen.findByRole("button", { name: /invite member/i }));
    const dialog = screen.getByRole("dialog", { name: "Invite member" });
    await userEvent.type(within(dialog).getByLabelText("Work email"), "maria@acme.test");
    await userEvent.type(within(dialog).getByLabelText("Name"), "Maria Santos");
    await userEvent.selectOptions(within(dialog).getByLabelText("Role for Brightside"), "brand_admin");
    await userEvent.click(within(dialog).getByRole("button", { name: /create invite link/i }));

    expect(await within(dialog).findByText("http://localhost:5173/invite/xyz")).toBeInTheDocument();
    const posts = api.calls.filter((c) => c.path === "/admin/v1/team/invites");
    expect(posts).toHaveLength(2); // rejected, then retried after the passkey
    expect(posts[1]!.body).toEqual({
      email: "maria@acme.test", name: "Maria Santos", company_role: "member", brand_roles: { "b-brightside": "brand_admin" },
    });
  });
});

describe("brands & domains", () => {
  it("adds a brand and opens its DNS records", async () => {
    const created = { ...brightside, slug: "nimbus", name: "Nimbus", id: "b-nimbus" };
    const api = fakeApi({
      ...signedIn(),
      "POST /admin/v1/brands": { status: 201, body: detail(created) },
      "GET /admin/v1/brands/nimbus": { status: 200, body: detail(created) },
    });
    const router = renderAt("/settings/brands/new");
    await userEvent.type(await screen.findByLabelText("Brand name"), "Nimbus Notes");
    expect(screen.getByLabelText("Short name (URL)")).toHaveValue("nimbus-notes");
    await userEvent.type(screen.getByLabelText("Sending domain"), "updates.nimbusnotes.app");
    await userEvent.type(screen.getByLabelText("Link domain"), "go.nimbusnotes.app");
    await userEvent.type(screen.getByLabelText("Postal address"), "1 Cloud Way");
    await userEvent.click(screen.getByRole("button", { name: "Add brand" }));

    expect(await screen.findByText("Sending is blocked until DNS is right")).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/settings/brands/nimbus");
    expect(api.calls.find((c) => c.method === "POST" && c.path === "/admin/v1/brands")?.body).toMatchObject({
      name: "Nimbus Notes", slug: "nimbus-notes", sending_domain: "updates.nimbusnotes.app",
    });
  });

  it("shows record statuses and re-checks DNS", async () => {
    const ready = detail({ ...brightside, status: "ready" });
    const api = fakeApi({
      ...signedIn(),
      "GET /admin/v1/brands/brightside": { status: 200, body: detail(brightside) },
      "POST /admin/v1/brands/brightside/dns-check": { status: 200, body: ready },
    });
    renderAt("/settings/brands/brightside");
    expect(await screen.findByText("record not found")).toBeInTheDocument();
    expect(screen.getByLabelText("Failing")).toBeInTheDocument();
    expect(screen.getByText("Setup needed")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /re-check now/i }));
    expect(await screen.findByText("Ready to send")).toBeInTheDocument();
    expect(api.calls.some((c) => c.path === "/admin/v1/brands/brightside/dns-check")).toBe(true);
  });
});
