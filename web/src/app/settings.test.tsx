import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter, RouterProvider } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import { routes } from "./routes";
import { brightside, fakeApi, kettle, meta, ownerMe } from "../test/fakeApi";
import type { ApiKey } from "../api/contactsApi";
import type { Company } from "../api/company";

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
  "GET /admin/v1/brands": { status: 200, body: { brands: [brightside, kettle] } },
  "GET /admin/v1/alerts": { status: 200, body: { alerts: [] } },
  "GET /admin/v1/approvals": { status: 200, body: { approvals: [] } },
};

const company: Company = {
  name: "",
  display_name: "Mail Console",
  installation: { version: "1.4.0", env: "production", debug: false, app_url: "https://mail.acme.com", passkey_domain: "acme.com", cloudflare_access: true },
};

const day = 86_400_000;
const key = (id: string, name: string, extra: Partial<ApiKey> = {}): ApiKey => ({
  id, name, prefix: `pk_${id}`, scopes: ["contacts:write"], ip_allowlist: [], created_at: new Date(Date.now() - 30 * day).toISOString(),
  expires_at: new Date(Date.now() + 60 * day).toISOString(), last_used_at: null, revoked_at: null, state: "active", ...extra,
});

describe("Settings → General", () => {
  it("shows the company name from meta in the sidebar, and lets an owner rename it", async () => {
    let name = "";
    const { calls } = fakeApi({
      ...signedIn,
      "GET /admin/v1/meta": () => ({ status: 200, body: { ...meta.body, company_name: name || "Mail Console" } }),
      "GET /admin/v1/company": () => ({ status: 200, body: { ...company, name, display_name: name || "Mail Console" } }),
      "PUT /admin/v1/company": (body) => {
        name = (body as { name: string }).name.trim();
        return { status: 200, body: { ...company, name, display_name: name } };
      },
    });
    const user = userEvent.setup();
    renderAt("/settings/general");
    const sidebar = await screen.findByRole("complementary");
    expect(await within(sidebar).findByText("Mail Console")).toBeInTheDocument();
    expect(await screen.findByText('Not set yet, so the app shows "Mail Console".')).toBeInTheDocument();
    expect(screen.getByText("acme.com")).toBeInTheDocument();
    expect(screen.getByText("On: a Cloudflare login is needed before the passkey")).toBeInTheDocument();

    const save = screen.getByRole("button", { name: "Save" });
    expect(save).toBeDisabled();
    await user.type(screen.getByLabelText("Company name"), "Acme Group");
    await user.click(save);
    expect(await screen.findByRole("status")).toHaveTextContent("Saved");
    expect(calls.find((c) => c.method === "PUT")?.body).toEqual({ name: "Acme Group" });
    expect(await within(sidebar).findByText("Acme Group")).toBeInTheDocument();
  });

  it("is read-only for everyone but owners", async () => {
    fakeApi({
      ...signedIn,
      "GET /admin/v1/me": { status: 200, body: { ...ownerMe, user: { ...ownerMe.user, company_role: "approver" } } },
      "GET /admin/v1/company": { status: 200, body: { ...company, name: "Acme Group", display_name: "Acme Group" } },
    });
    renderAt("/settings/general");
    expect(await screen.findByLabelText("Company name")).toBeDisabled();
    expect(screen.getByText("Only owners can change it.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save" })).not.toBeInTheDocument();
  });
});

describe("Settings → API keys", () => {
  it("lists every brand's keys, warns about ones about to expire, and links to each brand", async () => {
    fakeApi({
      ...signedIn,
      "GET /admin/v1/brands/brightside/api-keys": {
        status: 200,
        body: {
          scopes: [],
          keys: [
            key("a1", "Shop sign-up form", { expires_at: new Date(Date.now() + 5 * day).toISOString(), last_used_at: new Date().toISOString() }),
            key("a2", "Old site", { state: "revoked", revoked_at: new Date().toISOString() }),
          ],
        },
      },
      "GET /admin/v1/brands/kettle/api-keys": { status: 200, body: { scopes: [], keys: [key("b1", "Kettle checkout")] } },
    });
    renderAt("/settings/api-keys");
    const inUse = (await screen.findByRole("heading", { name: "In use" })).closest("section")!;
    expect(within(inUse).getByText("Shop sign-up form")).toBeInTheDocument();
    expect(within(inUse).getByText("Kettle checkout")).toBeInTheDocument();
    expect(within(inUse).queryByText("Old site")).not.toBeInTheDocument();
    expect(screen.getByText(/1 key expires within 14 days/)).toBeInTheDocument();
    const old = screen.getByRole("heading", { name: "Expired and revoked" }).closest("section")!;
    expect(within(old).getByText("Old site")).toBeInTheDocument();
    expect(screen.getAllByRole("link", { name: "Contacts API →" }).map((a) => a.getAttribute("href"))).toEqual([
      "/b/brightside/api",
      "/b/kettle/api",
    ]);
  });
});
