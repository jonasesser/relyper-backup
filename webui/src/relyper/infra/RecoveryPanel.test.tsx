// relyper: tests for the guided app recovery panel.
import { fireEvent, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithProviders } from "../../test/render";
import { RecoveryPanel } from "./RecoveryPanel";

const json = (body: unknown) =>
  new Response(JSON.stringify(body), {
    headers: { "Content-Type": "application/json" },
  });

const guide = (staging: string) => ({
  app: "compose:shop",
  plan: "app-shop",
  staging,
  order: ["db", "web"],
  steps: [
    { title: "Stop the app", commands: ["docker stop 'web'"] },
    {
      title: "Load the databases",
      commands: [`sh '${staging}/dumps/restore-databases.sh' db=db`],
    },
  ],
});

describe("RecoveryPanel", () => {
  let fetchMock: ReturnType<typeof vi.fn>;
  beforeEach(() => {
    fetchMock = vi.fn((url: string) => {
      if (url.includes("restore/stage"))
        return Promise.resolve(
          json({
            app: "compose:shop",
            plan: "app-shop",
            snapshot_id: "abcdef1234",
            snapshot_time: "2026-11-28T00:00:00Z",
            target: "/restore/compose-shop-1",
            operation_ids: [1],
            paths: ["/srv/shop"],
          }),
        );
      const staging = new URL(url).searchParams.get("staging") || "<staging>";
      return Promise.resolve(json(guide(staging)));
    });
    vi.stubGlobal("fetch", fetchMock);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("shows the runbook and fills in the staging directory", async () => {
    renderWithProviders(<RecoveryPanel app="compose:shop" />);
    expect(await screen.findByTestId("recovery-guide")).toHaveTextContent(
      "db → web",
    );
    expect(screen.getByTestId("recovery-guide")).toHaveTextContent(
      "<staging>/dumps/restore-databases.sh",
    );
    fireEvent.click(screen.getByTestId("stage-restore"));
    expect(await screen.findByTestId("stage-result")).toHaveTextContent(
      "/restore/compose-shop-1",
    );
    expect(
      await screen.findByText(/\/restore\/compose-shop-1\/dumps/),
    ).toBeInTheDocument();
  });
});
