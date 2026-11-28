// relyper: tests for the customer portal.
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithProviders } from "../../test/render";
import { PortalApp } from "./PortalApp";
import { portalTokenKey } from "./portalApi";

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });

const me = (admin: boolean) => ({
  tenant: { id: "acme", name: "ACME GmbH", escrow: false },
  user: { email: "kunde@acme.example", name: "Kunde" },
  admin,
  features: { email: true, storage: true, kits: true },
});

const overview = {
  tenant: { id: "acme", name: "ACME GmbH", escrow: false },
  plans: [
    {
      id: "acme-web",
      repo: "acme-repo",
      paths: ["/srv/acme"],
      schedule: "every 1 day(s)",
      state: "failing",
      ownStorage: false,
      snapshots: 3,
      lastSuccessAt: 1764000000000,
    },
  ],
  repos: [
    {
      id: "acme-repo",
      type: "rest",
      location: "rest:https://backup.example/acme",
      own: false,
    },
  ],
  problems: [
    { plan: "acme-web", state: "failing", message: "The last backup failed." },
  ],
};

const route = (admin: boolean) => (url: string, init?: RequestInit) => {
  const u = String(url);
  if (u.endsWith("api/me")) return Promise.resolve(json(me(admin)));
  if (u.endsWith("api/overview")) return Promise.resolve(json(overview));
  if (u.endsWith("api/notifications"))
    return Promise.resolve(
      json({
        notify: { recipients: ["ops@acme.example"], events: ["failure"] },
        events: ["failure", "warning", "success", "stale"],
        available: true,
      }),
    );
  if (u.includes("/backup") && init?.method === "POST")
    return Promise.resolve(json({ status: "queued" }, 202));
  if (u.includes("api/kits/"))
    return Promise.resolve(
      new Response("RELYPER BACKUP - RECOVERY KIT\nCustomer key:  abc\n"),
    );
  return Promise.resolve(json({ error: "not found" }, 404));
};

describe("PortalApp", () => {
  let fetchMock: ReturnType<typeof vi.fn>;
  beforeEach(() => {
    fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    window.history.replaceState(null, "", "/#/portal");
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    localStorage.clear();
  });

  it("asks to sign in without a portal session", async () => {
    renderWithProviders(<PortalApp />);
    expect(await screen.findByTestId("portal-login")).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("explains a refused sign-in", async () => {
    window.history.replaceState(
      null,
      "",
      "/?relyper_login_error=forbidden#/portal",
    );
    renderWithProviders(<PortalApp />);
    expect(await screen.findByTestId("portal-login-error")).toHaveTextContent(
      "no access",
    );
    expect(window.location.search).toBe("");
  });

  it("stores a portal session apart from the operator session", async () => {
    window.history.replaceState(null, "", "/?relyper_login=1#/portal");
    fetchMock.mockImplementation((url: string, init?: RequestInit) => {
      if (String(url).endsWith("auth/oidc/session"))
        return Promise.resolve(json({ token: "portal-token", kind: "portal" }));
      return route(false)(url, init);
    });
    renderWithProviders(<PortalApp />);
    expect(await screen.findByTestId("portal-tenant")).toHaveTextContent(
      "ACME GmbH",
    );
    expect(localStorage.getItem(portalTokenKey)).toBe("portal-token");
    expect(localStorage.getItem("backrest-ui-authToken")).toBeNull();
  });

  it("shows plans and problems; viewers get no admin buttons", async () => {
    localStorage.setItem(portalTokenKey, "t");
    fetchMock.mockImplementation(route(false));
    renderWithProviders(<PortalApp />);
    expect(await screen.findByTestId("plan-acme-web")).toHaveTextContent(
      "Failed",
    );
    expect(screen.getByTestId("problems")).toHaveTextContent(
      "The last backup failed.",
    );
    expect(screen.queryByTestId("backup-acme-web")).toBeNull();
    expect(screen.queryByTestId("kit-acme-repo")).toBeNull();
    expect(screen.queryByTestId("storage-add")).toBeNull();
    const call = fetchMock.mock.calls.find((c) =>
      String(c[0]).endsWith("api/overview"),
    );
    expect((call![1] as RequestInit).headers).toMatchObject({
      Authorization: "Bearer t",
    });
  });

  it("lets admins start a backup and create a recovery kit", async () => {
    localStorage.setItem(portalTokenKey, "t");
    fetchMock.mockImplementation(route(true));
    const createObjectURL = vi.fn(() => "blob:x");
    vi.stubGlobal(
      "URL",
      Object.assign(URL, { createObjectURL, revokeObjectURL: vi.fn() }),
    );
    renderWithProviders(<PortalApp />);
    fireEvent.click(await screen.findByTestId("backup-acme-web"));
    await waitFor(() =>
      expect(
        fetchMock.mock.calls.some((c) =>
          String(c[0]).includes("plans/acme-web/backup"),
        ),
      ).toBe(true),
    );
    fireEvent.click(screen.getByTestId("kit-acme-repo"));
    expect(await screen.findByTestId("kit-shown")).toBeInTheDocument();
    expect(createObjectURL).toHaveBeenCalled();
  });

  it("drops an expired session", async () => {
    localStorage.setItem(portalTokenKey, "old");
    const reload = vi.fn();
    vi.stubGlobal("location", {
      ...window.location,
      reload,
      search: "",
      hash: "#/portal",
      href: window.location.href,
    });
    fetchMock.mockResolvedValue(json({ error: "sign in" }, 401));
    renderWithProviders(<PortalApp />);
    await waitFor(() => expect(reload).toHaveBeenCalled());
    expect(localStorage.getItem(portalTokenKey)).toBeNull();
  });
});
