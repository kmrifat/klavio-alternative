import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter, RouterProvider } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import { routes } from "./routes";
import { brightside, fakeApi, meta, ownerMe } from "../test/fakeApi";
import type { Recovery, SafetyEvent } from "../api/brakes";
import type { Report } from "../api/feedback";

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

const paused = { ...brightside, status: "paused" as const, brake_state: "paused" as const };

const signedIn = {
  "GET /admin/v1/meta": meta,
  "GET /admin/v1/me": { status: 200, body: ownerMe },
  "GET /admin/v1/brands": { status: 200, body: { brands: [paused] } },
  "GET /admin/v1/alerts": { status: 200, body: { alerts: [] } },
  "GET /admin/v1/approvals": { status: 200, body: { approvals: [] } },
};

const pausedEvent: SafetyEvent = {
  id: 3, at: new Date().toISOString(), scope: "brand", brand_id: "b-brightside", campaign_id: null,
  action: "paused", from_state: "slowed", to_state: "paused", reason: "7-day bounces 4% ≥ 3%",
};

function recovery(extra: Partial<Recovery> = {}): Recovery {
  return {
    brand: paused,
    paused: true,
    event: pausedEvent,
    counts: { delivered: 960, bounces: 40, complaints: 0, unsubscribes: 3 },
    still_over: false,
    why: "",
    steps: [
      { id: "cause", title: "Find what caused it", help: "…" },
      { id: "list", title: "Clean the list", help: "…" },
    ],
    pending: null,
    events: [pausedEvent],
    ...extra,
  };
}

describe("recovery (S8)", () => {
  it("asks to resume once every step is done and a reason is given", async () => {
    let sent: unknown;
    fakeApi({
      ...signedIn,
      "GET /admin/v1/brands/brightside/recovery": { status: 200, body: recovery() },
      "POST /admin/v1/brands/brightside/recovery/resume": (body) => {
        sent = body;
        return { status: 202, body: { approval: { id: "a1" } } };
      },
    });
    renderAt("/b/brightside/health/recovery");
    expect(await screen.findByRole("region", { name: "Why it stopped" })).toHaveTextContent("7-day bounces 4% ≥ 3%");
    const ask = screen.getByRole("button", { name: "Ask to resume" });
    expect(ask).toBeDisabled();
    await userEvent.click(screen.getByLabelText(/Find what caused it/));
    await userEvent.click(screen.getByLabelText(/Clean the list/));
    expect(ask).toBeDisabled(); // no reason yet
    await userEvent.type(screen.getByLabelText("What you found and fixed"), "An old import; removed it.");
    await userEvent.click(ask);
    expect(await screen.findByRole("status")).toHaveTextContent("Request sent");
    expect(sent).toEqual({ steps: ["cause", "list"], reason: "An old import; removed it." });
  });

  it("explains when the rates are still over the line", async () => {
    fakeApi({
      ...signedIn,
      "GET /admin/v1/brands/brightside/recovery": { status: 200, body: recovery({ still_over: true, why: "7-day bounces 4% ≥ 3%" }) },
    });
    renderAt("/b/brightside/health/recovery");
    expect(await screen.findByText(/still over the pause line/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Ask to resume" })).toBeDisabled();
  });

  it("shows a waiting request instead of the form", async () => {
    fakeApi({
      ...signedIn,
      "GET /admin/v1/brands/brightside/recovery": {
        status: 200,
        body: recovery({ pending: { id: "a1", requested_at: new Date().toISOString(), summary: "Resume" } }),
      },
    });
    renderAt("/b/brightside/health/recovery");
    expect(await screen.findByText(/waiting for a second admin/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Ask to resume" })).toBeNull();
  });
});

describe("account brake", () => {
  it("lets an owner ask to resume all sending", async () => {
    let sent: unknown;
    fakeApi({
      ...signedIn,
      "GET /admin/v1/account-brake": { status: 200, body: { stopped_at: new Date().toISOString(), reason: "complaints across all brands 0.1% ≥ 0.08%", pending: null } },
      "POST /admin/v1/account-brake/release": (body) => {
        sent = body;
        return { status: 202, body: { approval: { id: "a2" } } };
      },
    });
    renderAt("/safety/account-brake");
    expect(await screen.findByText(/All sending stopped/)).toBeInTheDocument();
    const list = screen.getByRole("list", { name: "Recovery checklist" });
    for (const box of within(list).getAllByRole("checkbox")) await userEvent.click(box);
    await userEvent.type(screen.getByLabelText("What you found and fixed"), "Brightside's import; it's paused.");
    await userEvent.click(screen.getByRole("button", { name: "Ask to resume" }));
    expect(await screen.findByRole("status")).toHaveTextContent("Request sent");
    expect(sent).toEqual({ steps: ["cause", "brands", "amazon"], reason: "Brightside's import; it's paused." });
  });
});

function report(): Report {
  return {
    campaign: { id: "c1", name: "Autumn launch", state: "sending" } as Report["campaign"],
    sends: { queued: 400, sending: 0, sent: 600, failed: 2, unknown: 0, skipped: { brake_inactive: 3 } },
    feedback: { delivered: 590, bounces: 9, complaints: 1, unsubscribes: 2 },
    rates: { bounce: 0.015, complaint: 0.0017, unsubscribe: 0.0034 },
    limits: { bounce: 0.02, complaint: 0.0008, unsubscribe: 0.01 },
    engagement: {
      human_clicks: 41, unique_clickers: 33, bot_clicks: 12, bot_reasons: { too_fast_after_delivery: 9, scanner_user_agent: 3 },
      human_opens_est: 120, machine_opens: 80, soft_bounces: 1, delivery_delays: 0,
    },
    gate: { allowed: true, rate_factor: 1, needs_approval: false, block_inactive: true, reason: "Brightside is on watch" },
    safety_events: [],
    at: new Date().toISOString(),
    links: [{ link_no: 1, url: "https://shop.example.com/autumn", human_clicks: 41, unique_clickers: 33, bot_clicks: 12 }],
    providers: [{ provider: "Gmail", sent: 400, delivered: 395, bounces: 5, complaints: 1, clickers: 25 }],
  };
}

describe("campaign report (S11)", () => {
  it("shows people, not bots, against the brake lines", async () => {
    fakeApi({ ...signedIn, "GET /admin/v1/brands/brightside/campaigns/c1/report": { status: 200, body: report() } });
    renderAt("/b/brightside/campaigns/c1/report");
    expect(await screen.findByRole("heading", { name: "Campaign report" })).toBeInTheDocument();
    expect(screen.getByText("12 bot clicks filtered out (seconds after delivery: 9, security scanner: 3).")).toBeInTheDocument();
    expect(screen.getByRole("meter", { name: "Bounces" })).toHaveAttribute("aria-valuenow", "75");
    expect(screen.getByRole("meter", { name: "Complaints" })).toHaveAttribute("aria-valuenow", "100");
    expect(screen.getByText("https://shop.example.com/autumn")).toBeInTheDocument();
    expect(screen.getByText("Gmail").closest("tr")).toHaveTextContent("400395");
  });
});
