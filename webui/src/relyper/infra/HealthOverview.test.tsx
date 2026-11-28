// relyper: tests for the backup health summary on the dashboard.
import { screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithProviders } from "../../test/render";
import { HealthOverview } from "./HealthOverview";
import type { Overview } from "./infraApi";

const overview: Overview = {
  generated_at: "2026-11-28T08:00:00Z",
  level: "at_risk",
  docker_available: true,
  apps: { fully_protected: 2, backup_failed: 1, restore_not_verified: 1 },
  apps_total: 4,
  failed_24h: [
    {
      id: 7,
      kind: "backup",
      plan: "app-shop",
      repo: "hosteurope",
      time: "2026-11-28T02:30:00Z",
      message: "repository locked",
    },
  ],
  failed_count_24h: 1,
  running: [],
  last_successful_backup: {
    id: 5,
    kind: "backup",
    plan: "app-blog",
    repo: "hosteurope",
    time: "2026-11-28T02:00:00Z",
  },
  repos: [
    {
      repo: "hosteurope",
      location: "sftp:u@h:/b",
      type: "sftp",
      offsite: true,
      storage: {
        free_bytes: 20 * 1024 ** 3,
        total_bytes: 371 * 1024 ** 3,
        checked_at: "2026-11-28T03:00:00Z",
        source: "sftp",
        low: true,
      },
    },
  ],
  attention: ["1 app not protected", "storage of hosteurope is almost full"],
  backups_24h: { success: 3, error: 1 },
};

describe("HealthOverview", () => {
  beforeEach(() => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify(overview), {
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );
  });
  afterEach(() => vi.unstubAllGlobals());

  it("shows protection, failures and storage", async () => {
    renderWithProviders(<HealthOverview />);
    expect(await screen.findByTestId("tile-protected")).toHaveTextContent(
      "2 / 4",
    );
    expect(screen.getByTestId("tile-unprotected")).toHaveTextContent("1");
    expect(screen.getByTestId("tile-failed")).toHaveTextContent("1");
    expect(screen.getByTestId("health-attention")).toHaveTextContent(
      "storage of hosteurope is almost full",
    );
    expect(screen.getByText(/repository locked/)).toBeInTheDocument();
    expect(screen.getByText("almost full")).toBeInTheDocument();
  });

  it("links the app counts to the filtered Infrastructure page", async () => {
    renderWithProviders(<HealthOverview />);
    const link = await screen.findByRole("link", {
      name: "1 app not protected",
    });
    expect(link.getAttribute("href")).toBe("/infra?status=group:unprotected");
    expect(
      screen.getByTestId("tile-unprotected").closest("a")?.getAttribute("href"),
    ).toBe("/infra?status=group:unprotected");
  });
});
