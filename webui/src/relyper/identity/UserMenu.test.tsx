// relyper: tests for the user menu at the top right.
import { fireEvent, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderWithProviders } from "../../test/render";
import { operatorView, UserMenu } from "./UserMenu";

const stub = (body: unknown) =>
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(
      new Response(JSON.stringify(body), {
        headers: { "Content-Type": "application/json" },
      }),
    ),
  );

describe("UserMenu", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("shows the Relyper user, role, tenant and sign-out", async () => {
    stub({
      method: "relyper",
      user: "relyper:anna@example.com",
      name: "Anna Muster",
      email: "anna@example.com",
      roles: ["backup_user"],
      tenant: "relyper",
      provider_name: "Relyper",
      admin: false,
      read_only: true,
      provider: true,
      provider_enabled: true,
    });
    const out = vi.fn();
    renderWithProviders(<UserMenu onSignOut={out} />);
    const trigger = await screen.findByTestId("user-menu");
    expect(trigger).toHaveTextContent("Anna Muster");
    expect(screen.getByTestId("user-menu-badge")).toHaveTextContent(
      "Read-only",
    );
    fireEvent.click(trigger);
    const details = await screen.findByTestId("user-menu-details");
    expect(details).toHaveTextContent("anna@example.com");
    expect(details).toHaveTextContent("Tenant");
    expect(details).toHaveTextContent("backup_user");
    fireEvent.click(await screen.findByTestId("user-menu-sign-out"));
    expect(out).toHaveBeenCalled();
  });

  it("marks local users as emergency access when Relyper Identity is on", () => {
    const v = operatorView({
      method: "local",
      user: "admin",
      name: "admin",
      admin: true,
      read_only: false,
      provider: false,
      provider_enabled: true,
    });
    expect(v.badge?.label).toBe("Emergency");
    expect(v.rows[0].value).toContain("emergency");
  });

  it("falls back when the server cannot describe the user", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response("no", { status: 404 })),
    );
    renderWithProviders(
      <UserMenu onSignOut={() => {}} fallback={<span>Logout</span>} />,
    );
    expect(await screen.findByText("Logout")).toBeInTheDocument();
  });

  it("has no sign-out when authentication is off", () => {
    const v = operatorView({
      method: "none",
      admin: true,
      read_only: false,
      provider: false,
    });
    expect(v.title).toBe("No sign-in");
  });
});
