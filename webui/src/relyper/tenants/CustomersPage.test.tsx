// relyper: tests for the operator page "Customers".
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithProviders } from "../../test/render";
import { CustomersPage } from "./CustomersPage";

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });

const list = {
  tenants: [
    {
      id: "acme",
      name: "ACME GmbH",
      plans: ["acme-web", "other"],
      repos: ["acme-repo"],
      notify: { recipients: ["ops@acme.example"], events: ["failure"] },
    },
  ],
  plans: [
    { id: "acme-web", repo: "acme-repo", tenant: "acme" },
    { id: "other", repo: "ops-repo", tenant: "acme" },
    { id: "free", repo: "ops-repo" },
  ],
  repos: [{ id: "acme-repo", tenant: "acme" }, { id: "ops-repo" }],
  events: ["failure", "warning", "success", "stale"],
  status: {
    portal: true,
    email: false,
    escrow: false,
    tenantClaim: "tenant",
    customerRole: "backup_customer",
    customerAdmin: "backup_customer_admin",
  },
};

describe("CustomersPage", () => {
  let fetchMock: ReturnType<typeof vi.fn>;
  beforeEach(() => {
    fetchMock = vi.fn((url: string, init?: RequestInit) => {
      if (init?.method === "POST")
        return Promise.resolve(json({ id: "beta", name: "Beta", notify: {} }));
      return Promise.resolve(json(list));
    });
    vi.stubGlobal("fetch", fetchMock);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("lists customers and warns about plans whose repository is not assigned", async () => {
    renderWithProviders(<CustomersPage />);
    const card = await screen.findByTestId("tenant-acme");
    expect(card).toHaveTextContent("ACME GmbH");
    expect(card).toHaveTextContent("other (repository not assigned)");
    expect(screen.getByTestId("customers-status")).toHaveTextContent(
      "E-mail notifications",
    );
  });

  it("creates a customer", async () => {
    renderWithProviders(<CustomersPage />);
    fireEvent.change(await screen.findByTestId("tenant-id"), {
      target: { value: "beta" },
    });
    fireEvent.change(screen.getByTestId("tenant-name"), {
      target: { value: "Beta" },
    });
    fireEvent.click(screen.getByTestId("tenant-create"));
    await waitFor(() => {
      const post = fetchMock.mock.calls.find(
        (c) => (c[1] as RequestInit)?.method === "POST",
      );
      expect(post).toBeTruthy();
      expect(
        JSON.parse((post![1] as RequestInit).body as string),
      ).toMatchObject({ id: "beta", name: "Beta" });
    });
  });
});
