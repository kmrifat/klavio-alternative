import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter, RouterProvider } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import { routes } from "./routes";
import { brightside, fakeApi, meta, ownerMe } from "../test/fakeApi";
import type { Erasure, PersonData } from "../api/privacy";

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

const person: PersonData = {
  contact_ref: "j•••@example.com #8c87b489ce",
  contacts: [
    { brand_slug: "brightside", brand_name: "Brightside", brand_color: "#f97316", status: "subscribed", created_at: "2026-09-01T00:00:00Z" },
    { brand_slug: "northwind", brand_name: "Northwind", brand_color: "#2563eb", status: "unsubscribed", created_at: "2026-09-02T00:00:00Z" },
  ],
  suppression: null,
  erasures: [],
  sends: 4,
  clicks: 3,
  opens: 2,
  events: 5,
  consent_records: 2,
  audit_entries: 1,
};

const erasure: Erasure = {
  id: "er-1",
  contact_ref: person.contact_ref,
  reference: "Email to privacy@, 3 Oct",
  erased_by_name: "Shafayet H.",
  erased_at: new Date().toISOString(),
  summary: {
    brands: ["Brightside", "Northwind"], contacts_deleted: 2, clicks_blanked: 3, opens_blanked: 2, events_blanked: 5,
    send_errors_blanked: 0, api_replies_deleted: 0, imports_scrubbed: 0, exports_deleted: 0, audit_entries_redacted: 1, consent_records_kept: 2,
  },
};

describe("Settings → Data & privacy", () => {
  it("looks a person up, says what erasing does, and erases them after the address is typed again", async () => {
    let erased = false;
    const { calls } = fakeApi({
      ...signedIn,
      "GET /admin/v1/erasures": () => ({ status: 200, body: { erasures: erased ? [erasure] : [] } }),
      "POST /admin/v1/erasures/lookup": { status: 200, body: person },
      "POST /admin/v1/erasures": () => {
        erased = true;
        return { status: 201, body: { erasure } };
      },
    });
    const user = userEvent.setup();
    renderAt("/settings/privacy");
    expect(await screen.findByText("Nobody has been erased yet.")).toBeInTheDocument();

    await user.type(screen.getByLabelText("Email address"), "Jane@example.com");
    await user.click(screen.getByRole("button", { name: "Look up" }));
    expect(await screen.findByText("A contact on 2 brands:")).toBeInTheDocument();
    expect(screen.getByText(/Remove the IP address and browser from 3 clicks and 2 opens/)).toBeInTheDocument();
    expect(screen.getByText(/2 consent records/)).toBeInTheDocument();

    const erase = screen.getByRole("button", { name: "Erase permanently" });
    expect(erase).toBeDisabled();
    const reference = screen.getByLabelText("Where the request came from");
    await user.type(reference, "From jane@example.com");
    expect(screen.getByText("Leave the email address out of this.")).toBeInTheDocument();
    await user.clear(reference);
    await user.type(reference, "Email to privacy@, 3 Oct");
    await user.type(screen.getByLabelText("Type the address again to confirm"), "jane@example.org");
    expect(erase).toBeDisabled();
    await user.clear(screen.getByLabelText("Type the address again to confirm"));
    await user.type(screen.getByLabelText("Type the address again to confirm"), "jane@EXAMPLE.com");
    expect(erase).toBeEnabled();

    await user.click(erase);
    expect(await screen.findByRole("status")).toHaveTextContent("Erased j•••@example.com #8c87b489ce. Deleted from Brightside, Northwind.");
    expect(calls.find((c) => c.method === "POST" && c.path === "/admin/v1/erasures")?.body).toEqual({
      email: "Jane@example.com", confirm_email: "jane@EXAMPLE.com", reference: "Email to privacy@, 3 Oct",
    });
    expect(await screen.findByText("Email to privacy@, 3 Oct")).toBeInTheDocument();
    expect(screen.getByLabelText("Email address")).toHaveValue("");
  });

  it("tells members to ask an owner", async () => {
    fakeApi({ ...signedIn, "GET /admin/v1/me": { status: 200, body: { ...ownerMe, user: { ...ownerMe.user, company_role: "member" } } } });
    renderAt("/settings/privacy");
    expect(await screen.findByText(/Only owners and approvers can erase people/)).toBeInTheDocument();
  });
});
