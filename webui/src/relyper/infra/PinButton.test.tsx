// relyper: tests for pinning snapshots.
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithProviders } from "../../test/render";
import { PinButton } from "./PinButton";

describe("PinButton", () => {
  let fetchMock: ReturnType<typeof vi.fn>;
  beforeEach(() => {
    fetchMock = vi.fn(
      async () =>
        new Response(JSON.stringify({ pinned: true }), {
          headers: { "Content-Type": "application/json" },
        }),
    );
    vi.stubGlobal("fetch", fetchMock);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("pins a snapshot", async () => {
    const done = vi.fn();
    renderWithProviders(
      <PinButton repo="local" snapshot="abc12345" tags={[]} onDone={done} />,
    );
    expect(screen.queryByTestId("pinned-badge")).toBeNull();
    fireEvent.click(screen.getByTestId("pin-snapshot"));
    await waitFor(() => expect(done).toHaveBeenCalled());
    const [url, init] = fetchMock.mock.calls[0];
    expect(String(url)).toContain("/relyper/infra/snapshots/pin");
    expect(JSON.parse(String(init.body))).toEqual({
      repo: "local",
      snapshot: "abc12345",
      pinned: true,
    });
  });

  it("shows a pinned snapshot and unpins it", async () => {
    renderWithProviders(
      <PinButton repo="local" snapshot="abc12345" tags={["relyper-pinned"]} />,
    );
    expect(screen.getByTestId("pinned-badge")).toHaveTextContent("Pinned");
    fireEvent.click(screen.getByTestId("pin-snapshot"));
    await waitFor(() => expect(fetchMock).toHaveBeenCalled());
    expect(JSON.parse(String(fetchMock.mock.calls[0][1].body)).pinned).toBe(
      false,
    );
  });
});
