import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter, RouterProvider } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { routes } from "./routes";

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

describe("app shell", () => {
  beforeEach(() => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response(JSON.stringify({ version: "v-test", env: "development" }), { status: 200 })),
    );
  });
  afterEach(() => vi.unstubAllGlobals());

  it("redirects / to the Safety center in company view", async () => {
    const router = renderAt("/");
    expect(await screen.findByRole("heading", { name: "Safety center" })).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/safety");
    expect(screen.getByText("Company view")).toBeInTheDocument();
  });

  it("shows the brand as 'Sending as' on brand screens", async () => {
    renderAt("/b/kettle/contacts");
    expect(await screen.findByRole("heading", { name: "Contacts" })).toBeInTheDocument();
    expect(screen.getByText("Sending as")).toBeInTheDocument();
    expect(screen.getByText("mail.kettleandco.com")).toBeInTheDocument();
    expect(screen.getByText(/built in milestone M2/)).toBeInTheDocument();
  });

  it("links brand nav to the last brand used, even from company view", async () => {
    renderAt("/b/nimbus/campaigns");
    await screen.findByRole("heading", { name: "Campaigns" });
    await userEvent.click(within(screen.getByRole("navigation", { name: "Company" })).getByText("Audit log"));
    expect(await screen.findByRole("heading", { name: "Audit log" })).toBeInTheDocument();
    const brandNav = screen.getByRole("navigation", { name: "Brand" });
    expect(within(brandNav).getByText("Templates").closest("a")).toHaveAttribute("href", "/b/nimbus/templates");
  });

  it("switches brand from the switcher", async () => {
    const router = renderAt("/safety");
    await userEvent.click(await screen.findByRole("button", { name: /company view/i }));
    await userEvent.click(screen.getByRole("menuitemradio", { name: /brightside/i }));
    expect(router.state.location.pathname).toBe("/b/brightside/campaigns");
  });

  it("shows not found for an unknown brand", async () => {
    renderAt("/b/unknown/campaigns");
    expect(await screen.findByRole("heading", { name: "Page not found" })).toBeInTheDocument();
  });

  it("shows API status from /admin/v1/meta", async () => {
    renderAt("/safety");
    expect(await screen.findByText("API v-test")).toBeInTheDocument();
  });

  it("shows API offline when the API is unreachable", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => Promise.reject(new TypeError("Failed to fetch"))));
    renderAt("/safety");
    expect(await screen.findByText("API offline")).toBeInTheDocument();
  });
});
