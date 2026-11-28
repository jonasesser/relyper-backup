// relyper: tests for the storage use view.
import { screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithProviders } from "../../test/render";
import { StorageUsage, bytes } from "./StorageUsage";

describe("StorageUsage", () => {
  beforeEach(() => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        async () =>
          new Response(
            JSON.stringify([
              {
                repo: "hosteurope",
                location: "rclone:hosteurope:relyper",
                stored_bytes: 5 * 1024 ** 3,
                uncompressed_bytes: 8 * 1024 ** 3,
                snapshots: 40,
                stats_at: "2026-11-28T10:00:00Z",
                plans: [
                  {
                    plan: "app-budget",
                    apps: ["budget"],
                    snapshots: 7,
                    latest_size_bytes: 300 * 1024 ** 2,
                    latest_added_bytes: 2 * 1024 ** 2,
                    added_bytes: 320 * 1024 ** 2,
                  },
                  {
                    plan: "app-easypanel",
                    apps: ["Easypanel"],
                    snapshots: 7,
                    latest_size_bytes: 12 * 1024 ** 2,
                    latest_added_bytes: 40 * 1024,
                    added_bytes: 13 * 1024 ** 2,
                  },
                ],
              },
            ]),
            { status: 200, headers: { "Content-Type": "application/json" } },
          ),
      ),
    );
  });
  afterEach(() => vi.unstubAllGlobals());

  it("shows the repository size and what each plan stores", async () => {
    renderWithProviders(<StorageUsage />);
    const repo = await screen.findByTestId("usage-repo-hosteurope");
    expect(repo).toHaveTextContent("5.0 GB on the storage");
    const ep = screen.getByTestId("usage-plan-app-easypanel");
    expect(ep).toHaveTextContent("Easypanel");
    expect(ep).toHaveTextContent("12.0 MB");
    expect(ep).toHaveTextContent("+40.0 KB new");
  });

  it("formats sizes", () => {
    expect(bytes(0)).toBe("0 B");
    expect(bytes(1536)).toBe("1.5 KB");
    expect(bytes(300 * 1024 ** 2)).toBe("300 MB");
  });
});
