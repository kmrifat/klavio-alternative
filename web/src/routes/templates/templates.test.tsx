import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter, RouterProvider } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import { routes } from "../../app/routes";
import { brightside, fakeApi, meta, ownerMe } from "../../test/fakeApi";
import type { Me } from "../../api/types";
import type { LayoutResponse, Template, TemplateResponse } from "../../api/templates";

vi.mock("@simplewebauthn/browser", () => ({
  startAuthentication: vi.fn(async () => ({ id: "cred", type: "public-key" })),
  startRegistration: vi.fn(async () => ({ id: "cred", type: "public-key" })),
}));

afterEach(() => vi.unstubAllGlobals());

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

const as = (role: "editor" | "viewer"): Me => ({
  user: { id: "u-" + role, email: `${role}@acme.test`, name: role, company_role: "member" },
  brand_roles: { [brightside.id]: role },
  stepped_up_until: null,
});

const base = "/admin/v1/brands/brightside";
const tags = [{ name: "first_name", description: "Contact's first name", url: false }];

const layout: LayoutResponse = {
  layout: { mjml: "<mjml><mj-body>{{content}}</mj-body></mjml>", version: 0, is_default: true, updated_at: null },
  preview_html: "<p>layout preview</p>",
  issues: [],
  tags,
};

const tpl: Template = {
  id: "t1",
  name: "Autumn launch",
  mjml: "<mj-section></mj-section>",
  html: "<p>x</p>",
  text: "x",
  issues: [{ code: "link_not_allowed", severity: "error", message: "evil.example is not on this brand's link allowlist" }],
  error_count: 1,
  warning_count: 0,
  layout_version: 0,
  stale: false,
  version: 3,
  created_at: "2026-10-01T00:00:00Z",
  updated_at: new Date().toISOString(),
};
const tplResponse: TemplateResponse = { template: tpl, preview_html: "<p>Hi Alex</p>", preview_text: "Hi Alex", tags };

function api(me: Me, extra: Parameters<typeof fakeApi>[0] = {}) {
  return fakeApi({
    "GET /admin/v1/meta": meta,
    "GET /admin/v1/me": { status: 200, body: me },
    "GET /admin/v1/brands": { status: 200, body: { brands: [brightside] } },
    [`GET ${base}/layout`]: { status: 200, body: layout },
    [`GET ${base}/link-allowlist`]: { status: 200, body: { domains: [{ domain: "shop.example.com", added_by: null, created_at: "" }], brand_domains: ["news.brightside.co", "go.brightside.co"] } },
    [`GET ${base}/templates`]: {
      status: 200,
      body: {
        templates: [
          { id: "t1", name: "Autumn launch", error_count: 1, warning_count: 0, stale: false, updated_at: tpl.updated_at, updated_by_name: "Jon Park" },
          { id: "t3", name: "Old promo", error_count: 0, warning_count: 0, stale: true, updated_at: tpl.updated_at, updated_by_name: null },
        ],
      },
    },
    [`GET ${base}/templates/t1`]: { status: 200, body: tplResponse },
    ...extra,
  });
}

describe("templates list (S13)", () => {
  it("lists templates with their check status, the layout and the allowlist", async () => {
    api(ownerMe);
    renderAt("/b/brightside/templates");
    const row = (await screen.findByRole("link", { name: "Autumn launch" })).closest("tr")!;
    expect(within(row).getByText("1 error")).toBeInTheDocument();
    expect(within(row).getByText(/Jon Park/)).toBeInTheDocument();
    const stale = screen.getByRole("link", { name: "Old promo" }).closest("tr")!;
    expect(within(stale).getByText("Re-checking…")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /new template/i })).toBeInTheDocument();
    expect(await screen.findByText("Using the default layout.")).toBeInTheDocument();
    const allowed = await screen.findByRole("list", { name: "Allowed domains" });
    expect(within(allowed).getByText("shop.example.com")).toBeInTheDocument();
    expect(within(allowed).getByText("go.brightside.co · brand")).toBeInTheDocument();
  });

  it("lets brand admins allow a domain, and tells them templates were re-checked", async () => {
    const calls = api(ownerMe, {
      [`POST ${base}/link-allowlist`]: {
        status: 201,
        body: { domains: [], brand_domains: [], templates_queued: 3 },
      },
    }).calls;
    renderAt("/b/brightside/templates");
    await userEvent.type(await screen.findByLabelText("Domain to allow"), "blog.example.org");
    await userEvent.click(screen.getByRole("button", { name: "Allow" }));
    expect(await screen.findByText(/Re-checking 3 templates in the background/)).toBeInTheDocument();
    expect(calls.find((c) => c.method === "POST" && c.path.endsWith("/link-allowlist"))?.body).toEqual({ domain: "blog.example.org" });
  });

  it("hides editing from viewers", async () => {
    api(as("viewer"));
    renderAt("/b/brightside/templates");
    await screen.findByRole("link", { name: "Autumn launch" });
    expect(screen.queryByRole("button", { name: /new template/i })).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Domain to allow")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "View layout" })).toBeInTheDocument();
  });
});

describe("template editor", () => {
  it("creates a template and shows live check results", async () => {
    const calls = api(as("editor"), {
      [`POST ${base}/templates/preview`]: {
        status: 200,
        body: {
          preview_html: "<p>preview</p>",
          preview_text: "preview",
          issues: [{ code: "missing_alt", severity: "error", message: "image hero.png has no alt text", where: "", line: 0 }],
        },
      },
      [`POST ${base}/templates`]: { status: 201, body: { ...tplResponse, template: { ...tpl, id: "t2", name: "Welcome", version: 1 } } },
      [`GET ${base}/templates/t2`]: { status: 200, body: { ...tplResponse, template: { ...tpl, id: "t2", name: "Welcome", version: 1 } } },
    }).calls;
    const router = renderAt("/b/brightside/templates/new");

    expect(await screen.findByText("image hero.png has no alt text")).toBeInTheDocument();
    const frame = screen.getByTitle("Email preview");
    expect(frame).toHaveAttribute("sandbox", "");
    // Remote images are blocked until asked for.
    expect(frame.getAttribute("srcdoc")).toContain("img-src data:");
    await userEvent.click(screen.getByLabelText("Load images"));
    expect(screen.getByTitle("Email preview").getAttribute("srcdoc")).toContain("img-src https: http: data:");

    await userEvent.type(screen.getByLabelText("Template name"), "Welcome");
    await userEvent.click(screen.getByRole("button", { name: "Create template" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/b/brightside/templates/t2"));
    const created = calls.find((c) => c.method === "POST" && c.path === `${base}/templates`)!.body as { name: string; mjml: string };
    expect(created.name).toBe("Welcome");
    expect(created.mjml).toContain("<mj-section>");
  });

  it("saves with the version it started from and explains a conflict", async () => {
    const calls = api(as("editor"), {
      [`POST ${base}/templates/preview`]: { status: 200, body: { preview_html: "", preview_text: "", issues: [] } },
      [`PUT ${base}/templates/t1`]: { status: 409, body: { error: { code: "stale", message: "someone else saved a newer version" } } },
    }).calls;
    renderAt("/b/brightside/templates/t1");

    const name = await screen.findByLabelText("Template name");
    expect(screen.getByText("evil.example is not on this brand's link allowlist")).toBeInTheDocument();
    await userEvent.type(name, " v2");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText(/Someone else saved this template while you were editing/)).toBeInTheDocument();
    expect(calls.find((c) => c.method === "PUT")?.body).toMatchObject({ name: "Autumn launch v2", version: 3 });
  });

  it("is read-only for viewers and never compiles on their behalf", async () => {
    const calls = api(as("viewer")).calls;
    renderAt("/b/brightside/templates/t1");
    expect(await screen.findByLabelText("Template name")).toHaveAttribute("readonly");
    expect(screen.queryByRole("button", { name: "Save" })).not.toBeInTheDocument();
    await new Promise((r) => setTimeout(r, 600));
    expect(calls.some((c) => c.path.endsWith("/preview"))).toBe(false);
  });
});

describe("layout editor", () => {
  it("says when a change waits for a second admin", async () => {
    api(ownerMe, {
      [`POST ${base}/layout/preview`]: { status: 200, body: { ...layout, issues: [] } },
      [`PUT ${base}/layout`]: { status: 202, body: { approval: { id: "ap1", status: "pending", expires_at: "2026-10-17T00:00:00Z" } } },
    });
    renderAt("/b/brightside/templates/layout");
    await userEvent.type(await screen.findByLabelText("MJML source"), " ");
    const save = screen.getByRole("button", { name: "Save layout" });
    await waitFor(() => expect(save).toBeEnabled());
    await userEvent.click(save);
    expect(await screen.findByText(/Sent for approval/)).toBeInTheDocument();
  });

  it("is read-only below brand admin", async () => {
    api(as("editor"));
    renderAt("/b/brightside/templates/layout");
    expect(await screen.findByText("Only brand admins can change the layout.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save layout" })).not.toBeInTheDocument();
  });

  it("blocks saving while checks fail", async () => {
    api(ownerMe, {
      [`POST ${base}/layout/preview`]: {
        status: 200,
        body: { ...layout, issues: [{ code: "missing_unsubscribe", severity: "error", message: "add an unsubscribe link" }] },
      },
    });
    renderAt("/b/brightside/templates/layout");
    const editor = await screen.findByLabelText("MJML source");
    await userEvent.type(editor, " ");
    expect(await screen.findByText("add an unsubscribe link")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save layout" })).toBeDisabled();
  });
});
