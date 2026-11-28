// relyper: with Relyper Identity active, local users are emergency access
// and the login dialog hides their fields until requested.
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderWithProviders } from "../../test/render";
import { LoginModal } from "../../features/auth/LoginModal";

const stub = (enabled: boolean) =>
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ enabled, name: "Relyper" }), {
        headers: { "Content-Type": "application/json" },
      }),
    ),
  );

describe("login with Relyper Identity", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("hides the local fields behind emergency access", async () => {
    stub(true);
    renderWithProviders(<LoginModal />);
    expect(
      await screen.findByTestId("relyper-login-button"),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(screen.queryByTestId("login-username")).toBeNull(),
    );
    expect(screen.queryByTestId("login-submit")).toBeNull();
    fireEvent.click(screen.getByTestId("relyper-breakglass"));
    expect(await screen.findByTestId("login-username")).toBeInTheDocument();
    expect(screen.getByTestId("login-submit")).toBeInTheDocument();
  });

  it("shows the local fields when Relyper Identity is off", async () => {
    stub(false);
    renderWithProviders(<LoginModal />);
    expect(await screen.findByTestId("login-username")).toBeInTheDocument();
    expect(screen.queryByTestId("relyper-breakglass")).toBeNull();
  });
});
