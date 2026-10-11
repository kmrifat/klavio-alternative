import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter, RouterProvider } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import { routes } from "../../app/routes";
import { brightside, fakeApi, meta, ownerMe } from "../../test/fakeApi";
import type { Me } from "../../api/types";
import type { Campaign, CampaignResponse, Review } from "../../api/campaigns";

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

const viewer: Me = {
  user: { id: "u-v", email: "v@acme.test", name: "Vee", company_role: "member" },
  brand_roles: { [brightside.id]: "viewer" },
  stepped_up_until: null,
} as Me;

// A company approver has no brand role but sees every brand read-only.
const approver: Me = {
  user: { id: "u-a", email: "ap@acme.test", name: "Ada", company_role: "approver" },
  brand_roles: {},
  stepped_up_until: null,
} as Me;

const base = "/admin/v1/brands/brightside";

const draft: Campaign = {
  id: "c1",
  name: "Autumn launch",
  topic_id: "t1",
  template_id: "tp1",
  subject: "Hi {{first_name}}",
  preheader: "",
  include_at_risk: false,
  state: "draft",
  paused_from: null,
  pause_reason: "",
  waiting_reason: "",
  version: 2,
  from_header: "",
  test_slice_pct: null,
  hold_minutes: null,
  scheduled_at: null,
  start_at: null,
  confirmed_at: null,
  approval_id: null,
  approval_reasons: [],
  hold_until: null,
  audience_count: 0,
  excluded_count: 0,
  updated_at: new Date().toISOString(),
  finished_at: null,
};

const draftResponse: CampaignResponse = {
  campaign: draft,
  audience: { included: 2431, excluded: { suppressed: 12, sunset: 340, unsubscribed: 51 }, first_time_imported: 0, at_risk: 0 },
};

const review: Review = {
  audience: draftResponse.audience!,
  approval_reasons: [],
  problems: [],
  test_slice_pct: 5,
  hold_minutes: 45,
  from_header: '"Brightside" <hello@news.brightside.co>',
  confirm_text: "2431",
  cancel_window_minutes: 10,
};

function api(me: Me, extra: Parameters<typeof fakeApi>[0] = {}) {
  return fakeApi({
    "GET /admin/v1/meta": meta,
    "GET /admin/v1/me": { status: 200, body: me },
    "GET /admin/v1/brands": { status: 200, body: { brands: [brightside] } },
    [`GET ${base}/campaigns`]: {
      status: 200,
      body: { campaigns: [draft, { ...draft, id: "c2", name: "Spring sale", state: "done", audience_count: 1800 }] },
    },
    [`GET ${base}/campaigns/c1`]: { status: 200, body: draftResponse },
    [`GET ${base}/campaigns/c1/review`]: { status: 200, body: review },
    [`GET ${base}/topics`]: { status: 200, body: { topics: [{ id: "t1", name: "Newsletter", subscribers: 2834 }] } },
    [`GET ${base}/templates`]: {
      status: 200,
      body: { templates: [{ id: "tp1", name: "Autumn", error_count: 0, warning_count: 0, stale: false, updated_at: "", updated_by_name: null }] },
    },
    [`GET ${base}/seeds`]: { status: 200, body: { seeds: [{ email: "team@brightside.co", created_at: "" }] } },
    [`GET ${base}/sender`]: { status: 200, body: { sender: { from_name: "", from_local: "hello", reply_to: "" }, from_header: review.from_header } },
    ...extra,
  });
}

describe("campaigns (S10)", () => {
  it("lists campaigns with their status, and offers a new one to editors", async () => {
    api(ownerMe);
    renderAt("/b/brightside/campaigns");
    const row = (await screen.findByRole("link", { name: "Spring sale" })).closest("tr")!;
    expect(within(row).getByText("Sent")).toBeInTheDocument();
    expect(within(row).getByText("1,800")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /new campaign/i })).toBeInTheDocument();
    expect(await screen.findByText(review.from_header)).toBeInTheDocument();
  });

  it("shows who gets a draft and why others are left out (S5)", async () => {
    api(ownerMe);
    renderAt("/b/brightside/campaigns/c1");
    expect(await screen.findByText("2,431")).toBeInTheDocument();
    const left = screen.getByRole("list", { name: "Left out" });
    expect(within(left).getByText("No sign of interest for 6+ months")).toBeInTheDocument();
    expect(within(left).getByText("340")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /review and schedule/i })).toBeInTheDocument();
  });

  it("sends a test to the seed list", async () => {
    const calls = api(ownerMe, { [`POST ${base}/campaigns/c1/test`]: { status: 200, body: { sent: 1, to: ["team@brightside.co"] } } }).calls;
    renderAt("/b/brightside/campaigns/c1");
    await userEvent.click(await screen.findByRole("button", { name: "Send test" }));
    expect(await screen.findByText("Sent to 1 address.")).toBeInTheDocument();
    expect(calls.some((c) => c.method === "POST" && c.path.endsWith("/test"))).toBe(true);
  });

  it("hides editing from viewers", async () => {
    api(viewer);
    renderAt("/b/brightside/campaigns/c1");
    expect(await screen.findByLabelText("Campaign name")).toHaveAttribute("readonly");
    expect(screen.queryByRole("button", { name: /review and schedule/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Send test" })).not.toBeInTheDocument();
  });

  it("shows campaigns read-only to company approvers", async () => {
    api(approver);
    renderAt("/b/brightside/campaigns");
    expect(await screen.findByRole("link", { name: "Spring sale" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /new campaign/i })).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("link", { name: "Autumn launch" }));
    expect(await screen.findByText("2,431")).toBeInTheDocument();
    expect(screen.getByLabelText("Campaign name")).toHaveAttribute("readonly");
    expect(screen.queryByRole("button", { name: /review and schedule/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });
});

describe("confirmation (S6)", () => {
  it("schedules only after the number of people is typed", async () => {
    // Like the real API: once scheduled, the campaign reads back as scheduled.
    const scheduled: CampaignResponse = {
      campaign: { ...draft, state: "scheduled", start_at: new Date(Date.now() + 600_000).toISOString() },
      breakdown: {},
      progress: { test: {}, rest: {} },
    };
    let done = false;
    const calls = api(ownerMe, {
      [`POST ${base}/campaigns/c1/schedule`]: () => {
        done = true;
        return { status: 200, body: scheduled };
      },
      [`GET ${base}/campaigns/c1`]: () => ({ status: 200, body: done ? scheduled : draftResponse }),
    }).calls;
    const router = renderAt("/b/brightside/campaigns/c1/review");
    const send = await screen.findByRole("button", { name: "Send to 2,431 people" });
    expect(send).toBeDisabled();
    await userEvent.type(screen.getByLabelText(/Type 2,431 to confirm/), "2,431");
    expect(send).toBeEnabled();
    await userEvent.click(send);
    await waitFor(() => expect(router.state.location.pathname).toBe("/b/brightside/campaigns/c1"));
    expect(calls.find((c) => c.path.endsWith("/schedule"))?.body).toMatchObject({ version: 2, confirm: "2,431" });
    expect(await screen.findByText(/Until then you can still cancel it/)).toBeInTheDocument();
  });

  it("lists what blocks scheduling and hides the confirm form", async () => {
    api(ownerMe, {
      [`GET ${base}/campaigns/c1/review`]: { status: 200, body: { ...review, problems: ["add a subject line"] } },
    });
    renderAt("/b/brightside/campaigns/c1/review");
    expect(await screen.findByText("add a subject line")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Send to/ })).not.toBeInTheDocument();
  });

  it("explains when a second person must approve", async () => {
    api(ownerMe, {
      [`GET ${base}/campaigns/c1/review`]: {
        status: 200,
        body: { ...review, approval_reasons: [{ code: "at_risk_included", text: "includes at-risk contacts" }] },
      },
    });
    renderAt("/b/brightside/campaigns/c1/review");
    expect(await screen.findByText(/needs a second person's approval, because it includes at-risk contacts/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Ask for approval" })).toBeInTheDocument();
  });
});

describe("a running campaign", () => {
  it("shows progress and lets a sender pause it", async () => {
    const running: CampaignResponse = {
      campaign: { ...draft, state: "sending", audience_count: 100, test_slice_pct: 5, hold_minutes: 45 },
      breakdown: { included_test: 5, included_rest: 95, sunset: 3 },
      progress: { test: { sent: 5 }, rest: { sent: 40, queued: 54, skipped: 1 } },
    };
    const calls = api(ownerMe, {
      [`GET ${base}/campaigns/c1`]: { status: 200, body: running },
      [`POST ${base}/campaigns/c1/pause`]: {
        status: 200,
        body: { ...running, campaign: { ...running.campaign, state: "paused", pause_reason: "paused by Olivia" } },
      },
    }).calls;
    renderAt("/b/brightside/campaigns/c1");
    expect(await screen.findByText("45")).toBeInTheDocument(); // sent
    expect(screen.getByText("54")).toBeInTheDocument(); // waiting
    await userEvent.click(screen.getByRole("button", { name: "Pause" }));
    expect(await screen.findByText("Paused: paused by Olivia")).toBeInTheDocument();
    expect(calls.some((c) => c.path.endsWith("/pause"))).toBe(true);
  });
});
