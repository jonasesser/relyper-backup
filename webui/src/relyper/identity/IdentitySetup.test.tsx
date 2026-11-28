// relyper: tests for the OIDC sign-in status card.
import { screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderWithProviders } from "../../test/render";
import { IdentitySetup } from "./IdentitySetup";

const base = {
  configured: true,
  enabled: false,
  local_auth: true,
  issuer: "https://id.example",
  client_id: "backup",
  client_secret_set: true,
  callback_url: "https://backup.example/auth/oidc/callback",
  public_url_set: true,
  login_role: "backup_user",
  admin_role: "backup_admin",
  scopes: ["openid"],
  display_name: "Relyper",
  session_hours: 8,
  variables: ["RELYPER_OIDC_ISSUER"],
  required_claims: "roles",
};

const stub = (body: unknown) =>
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(
      new Response(JSON.stringify(body), {
        headers: { "Content-Type": "application/json" },
      }),
    ),
  );

describe("IdentitySetup", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("shows active sign-in and the callback URL", async () => {
    stub({ ...base, enabled: true });
    renderWithProviders(<IdentitySetup />);
    expect(await screen.findByTestId("identity-state")).toHaveTextContent(
      "active",
    );
    expect(screen.getByTestId("identity-callback")).toHaveTextContent(
      "https://backup.example/auth/oidc/callback",
    );
  });

  it("explains a configuration error", async () => {
    stub({ ...base, error: "RELYPER_OIDC_CLIENT_ID is required" });
    renderWithProviders(<IdentitySetup />);
    expect(await screen.findByTestId("identity-state")).toHaveTextContent(
      "configuration error",
    );
    expect(
      screen.getByText("RELYPER_OIDC_CLIENT_ID is required"),
    ).toBeInTheDocument();
  });

  it("says when authentication is off", async () => {
    stub({ ...base, local_auth: false });
    renderWithProviders(<IdentitySetup />);
    expect(await screen.findByTestId("identity-state")).toHaveTextContent(
      "not offered",
    );
  });
});
