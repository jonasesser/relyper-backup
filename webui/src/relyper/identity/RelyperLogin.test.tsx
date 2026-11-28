// relyper: tests for sign-in through Relyper Identity.
import { screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithProviders } from "../../test/render";
import { RelyperLogin } from "./RelyperLogin";
import * as client from "../../api/client";

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });

describe("RelyperLogin", () => {
  let fetchMock: ReturnType<typeof vi.fn>;
  beforeEach(() => {
    fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    window.history.replaceState(null, "", "/");
  });

  it("renders nothing when sign-in through Relyper is off", async () => {
    fetchMock.mockResolvedValue(json({ enabled: false }));
    renderWithProviders(<RelyperLogin />);
    await waitFor(() => expect(fetchMock).toHaveBeenCalled());
    expect(screen.queryByTestId("relyper-login-button")).toBeNull();
  });

  it("offers the button with the provider name", async () => {
    fetchMock.mockResolvedValue(json({ enabled: true, name: "Relyper" }));
    renderWithProviders(<RelyperLogin />);
    expect(await screen.findByTestId("relyper-login-button")).toHaveTextContent(
      "Sign in with Relyper",
    );
    expect(
      screen.getByText("Emergency access with a local user"),
    ).toBeInTheDocument();
  });

  it("fetches the token once after the provider redirected back", async () => {
    window.history.replaceState(null, "", "/?relyper_login=1");
    const setToken = vi.spyOn(client, "setAuthToken");
    fetchMock.mockImplementation((url: string, init?: RequestInit) => {
      if (String(url).endsWith("auth/oidc/session")) {
        expect(init?.method).toBe("POST");
        expect(
          (init?.headers as Record<string, string>)["X-Relyper-Login"],
        ).toBe("1");
        return Promise.resolve(json({ token: "jwt-token" }));
      }
      return Promise.resolve(json({ enabled: true, name: "Relyper" }));
    });
    renderWithProviders(<RelyperLogin />);
    await waitFor(() => expect(setToken).toHaveBeenCalledWith("jwt-token"));
    expect(window.location.search).toBe("");
  });

  it("explains a refused sign-in", async () => {
    window.history.replaceState(null, "", "/?relyper_login_error=forbidden");
    fetchMock.mockResolvedValue(json({ enabled: true, name: "Relyper" }));
    renderWithProviders(<RelyperLogin />);
    expect(await screen.findByTestId("relyper-login-error")).toHaveTextContent(
      "no access",
    );
    expect(window.location.search).toBe("");
  });
});
