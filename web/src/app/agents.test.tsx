import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter, RouterProvider } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import { routes } from "./routes";
import { brightside, fakeApi, meta, ownerMe } from "../test/fakeApi";
import type { AgentToken } from "../api/agents";
import type { OAuthGrant } from "../api/oauth";

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

const token = (id: string, name: string, extra: Partial<AgentToken> = {}): AgentToken => ({
  id, name, prefix: `kag_${id}`, created_at: new Date().toISOString(), expires_at: new Date(Date.now() + 30 * 86_400_000).toISOString(),
  revoked_at: null, last_used_at: null, state: "active", oauth_client_id: null, ...extra,
});

describe("Settings → AI agents", () => {
  it("creates a token, shows it once with each agent's setup, and revokes", async () => {
    let tokens: AgentToken[] = [token("old1", "Codex at work", { state: "revoked", revoked_at: new Date().toISOString() })];
    const { calls } = fakeApi({
      ...signedIn,
      "GET /admin/v1/agent-tokens": () => ({ status: 200, body: { tokens } }),
      "POST /admin/v1/agent-tokens": (body) => {
        const t = token("new1", (body as { name: string }).name);
        tokens = [t, ...tokens];
        return { status: 201, body: { token: t, secret: "kag_new1_s3cret" } };
      },
      "POST /admin/v1/agent-tokens/new1/revoke": () => {
        tokens = tokens.map((t) => (t.id === "new1" ? { ...t, state: "revoked" as const } : t));
        return { status: 200, body: { token: tokens[0] } };
      },
    });
    const user = userEvent.setup();
    renderAt("/settings/agents");
    expect(await screen.findByText("Codex at work")).toBeInTheDocument();

    await user.type(screen.getByLabelText("Name"), "Claude Code on my laptop");
    await user.selectOptions(screen.getByLabelText("Expires after"), "7");
    await user.click(screen.getByRole("button", { name: "Create token" }));
    expect(await screen.findByText('"Claude Code on my laptop" is ready')).toBeInTheDocument();
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({ name: "Claude Code on my laptop", expires_in_days: 7 });
    expect(screen.getByText("kag_new1_s3cret")).toBeInTheDocument();
    expect(screen.getByText(/claude mcp add --transport http mail-console .*\/mcp --header "Authorization: Bearer kag_new1_s3cret"/)).toBeInTheDocument();
    await user.click(screen.getByRole("tab", { name: "OpenCode" }));
    expect(screen.getByText(/"type": "remote"/)).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "I've copied it" }));
    expect(screen.queryByText("kag_new1_s3cret")).not.toBeInTheDocument();
    const row = (await screen.findByText("Claude Code on my laptop")).closest("tr")!;
    await user.click(within(row).getByRole("button", { name: "Revoke Claude Code on my laptop" }));
    expect(await within((await screen.findByText("Claude Code on my laptop")).closest("tr")!).findByText("Revoked")).toBeInTheDocument();
  });

  it("marks what an agent did in the audit log", async () => {
    fakeApi({
      ...signedIn,
      "GET /admin/v1/audit": {
        status: 200,
        body: {
          entries: [{ id: 1, at: new Date().toISOString(), actor_email: "owner@acme.test", brand_id: null, action: "campaign.created",
            target_type: "campaign", target_id: "c1", ip: "", via_agent: "Claude Code (kag_abc)" }],
        },
      },
    });
    renderAt("/audit");
    expect(await screen.findByText("via AI agent Claude Code (kag_abc)")).toBeInTheDocument();
  });
});

describe("Connecting Claude (/connect/:id)", () => {
  const grant: OAuthGrant = { id: "g1", client_name: "Claude", redirect_host: "claude.ai", expires_at: new Date(Date.now() + 600_000).toISOString(), status: "pending", token_days: 90 };

  it("says who asks and where you'll go back to, and approves with a passkey", async () => {
    const assign = vi.fn();
    vi.stubGlobal("location", { ...window.location, assign });
    const { calls } = fakeApi({
      ...signedIn,
      "GET /admin/v1/oauth/grants/g1": { status: 200, body: grant },
      "POST /admin/v1/oauth/grants/g1/approve": { status: 200, body: { redirect_to: "https://claude.ai/api/mcp/auth_callback?code=abc&state=xyz" } },
    });
    const user = userEvent.setup();
    renderAt("/connect/g1");
    expect(await screen.findByText("Connect Claude to Acme Group?")).toBeInTheDocument();
    expect(screen.getByText("claude.ai")).toBeInTheDocument();
    expect(screen.getByText(/for 90 days/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Allow with passkey" }));
    expect(await screen.findByText("Sending you back to claude.ai…")).toBeInTheDocument();
    expect(calls.some((c) => c.method === "POST" && c.path === "/admin/v1/oauth/grants/g1/approve")).toBe(true);
    expect(assign).toHaveBeenCalledWith("https://claude.ai/api/mcp/auth_callback?code=abc&state=xyz");
  });

  it("explains an expired request", async () => {
    fakeApi({ ...signedIn, "GET /admin/v1/oauth/grants/g1": { status: 200, body: { ...grant, status: "expired" } } });
    renderAt("/connect/g1");
    expect(await screen.findByText(/This request expired/)).toBeInTheDocument();
  });
});
