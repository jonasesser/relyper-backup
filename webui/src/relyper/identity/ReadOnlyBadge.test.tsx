// relyper: tests for the read-only header badge.
import { screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderWithProviders } from "../../test/render";
import { ReadOnlyBadge } from "./ReadOnlyBadge";

const stub = (body: unknown) =>
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(
      new Response(JSON.stringify(body), {
        headers: { "Content-Type": "application/json" },
      }),
    ),
  );

describe("ReadOnlyBadge", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("shows for read-only users", async () => {
    stub({ read_only: true, provider: true, user: "u@example.com" });
    renderWithProviders(<ReadOnlyBadge />);
    expect(await screen.findByTestId("read-only-badge")).toHaveTextContent(
      "Read-only",
    );
  });

  it("stays hidden for admins", async () => {
    const f = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ read_only: false, provider: true }), {
        headers: { "Content-Type": "application/json" },
      }),
    );
    vi.stubGlobal("fetch", f);
    renderWithProviders(<ReadOnlyBadge />);
    await waitFor(() => expect(f).toHaveBeenCalled());
    expect(screen.queryByTestId("read-only-badge")).toBeNull();
  });
});
