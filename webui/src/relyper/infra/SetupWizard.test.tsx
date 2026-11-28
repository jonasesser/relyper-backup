// relyper: tests for the backup setup wizard.
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithProviders } from "../../test/render";
import { ConfigSchema, RepoSchema } from "../../../gen/ts/v1/config_pb";
import { SetupWizard } from "./SetupWizard";

const json = (body: unknown) =>
  new Response(JSON.stringify(body), {
    headers: { "Content-Type": "application/json" },
  });

const proposals = {
  apps: [
    {
      app: "compose:shop",
      name: "shop",
      kind: "compose",
      existing_plan: false,
      has_databases: true,
      plan: {
        app: "compose:shop",
        plan_id: "app-shop",
        paths: ["/srv/shop"],
        databases: ["db (postgres)"],
      },
      size: { bytes: 3 * 1024 ** 3, files: 1200, complete: true },
    },
    {
      app: "compose:blog",
      name: "blog",
      kind: "compose",
      existing_plan: true,
      has_databases: false,
      plan: { app: "compose:blog", plan_id: "app-blog", paths: ["/srv/blog"] },
      size: { bytes: 1024, files: 1, complete: false },
    },
  ],
  total_bytes: 3 * 1024 ** 3 + 1024,
  complete: false,
  storage: {
    hosteurope: {
      free_bytes: 300 * 1024 ** 3,
      total_bytes: 371 * 1024 ** 3,
      checked_at: "2026-11-28T03:00:00Z",
      source: "sftp",
      low: false,
    },
  },
  defaults: {
    files_cron: "30 2 * * *",
    database_cron: "15 */6 * * *",
    daily: 7,
    weekly: 4,
    monthly: 12,
    yearly: 0,
    compression: "auto",
  },
};

describe("SetupWizard", () => {
  let fetchMock: ReturnType<typeof vi.fn>;
  beforeEach(() => {
    fetchMock = vi.fn((url: string, init?: RequestInit) => {
      if (url.endsWith("setup/propose"))
        return Promise.resolve(json(proposals));
      if (url.endsWith("setup/create"))
        return Promise.resolve(
          json([{ app: "compose:shop", plan_id: "app-shop" }]),
        );
      return Promise.resolve(json({}));
    });
    vi.stubGlobal("fetch", fetchMock);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("proposes plans with sizes and creates the selected ones", async () => {
    const config = create(ConfigSchema, {
      repos: [create(RepoSchema, { id: "hosteurope", uri: "sftp:u@h:/b" })],
    });
    renderWithProviders(
      <SetupWizard apps={[]} easypanel={false} onDone={() => {}} />,
      { config },
    );
    fireEvent.click(screen.getByTestId("setup-preview"));
    expect(await screen.findByTestId("setup-proposals")).toHaveTextContent(
      "3.0 GB in 1200 files",
    );
    expect(screen.getByText("plan exists")).toBeInTheDocument();
    // The existing plan is not selected; the estimate counts the shop only.
    expect(screen.getByTestId("setup-estimate")).toHaveTextContent(
      "Data to back up: 3.0 GB",
    );
    expect(screen.getByTestId("setup-estimate")).toHaveTextContent(
      "Free in hosteurope: 300.0 GB",
    );
    fireEvent.click(screen.getByTestId("setup-create"));
    expect(await screen.findByTestId("setup-results")).toHaveTextContent(
      "plan app-shop created",
    );
    const call = fetchMock.mock.calls.find((c) =>
      String(c[0]).endsWith("setup/create"),
    )!;
    const body = JSON.parse(String(call[1]!.body));
    expect(body).toMatchObject({
      apps: ["compose:shop"],
      repo: "hosteurope",
      options: { database_cron: "15 */6 * * *", monthly: 12 },
    });
    await waitFor(() =>
      expect(screen.queryByTestId("setup-proposals")).toBeNull(),
    );
  });

  it("offers an Easypanel-only plan and lists what is in it", async () => {
    fetchMock.mockImplementation((url: string) =>
      Promise.resolve(
        json(
          url.endsWith("setup/propose")
            ? {
                ...proposals,
                apps: [
                  {
                    app: "system:easypanel",
                    name: "Easypanel",
                    kind: "system",
                    existing_plan: false,
                    has_databases: false,
                    plan: {
                      app: "system:easypanel",
                      plan_id: "app-system-easypanel",
                      paths: ["/etc/easypanel/traefik"],
                      sources: [
                        {
                          service: "web/site",
                          source: "upload",
                          code: true,
                          reason: "uploaded",
                        },
                        {
                          service: "web/ghost",
                          source: "image",
                          code: false,
                          reason: "runs the image ghost:5",
                        },
                      ],
                    },
                    size: { bytes: 5 * 1024 ** 2, files: 40, complete: true },
                    items: [
                      {
                        path: "/etc/easypanel/traefik",
                        label: "Easypanel: Traefik routes and TLS certificates",
                        bytes: 4 * 1024 ** 2,
                        files: 30,
                        complete: true,
                      },
                    ],
                  },
                ],
              }
            : {},
        ),
      ),
    );
    renderWithProviders(
      <SetupWizard apps={[]} easypanel={true} onDone={() => {}} />,
    );
    fireEvent.change(screen.getByTestId("setup-scope"), {
      target: { value: "easypanel_config" },
    });
    expect(screen.getByTestId("setup-scope-help")).toHaveTextContent(
      "code only when it was uploaded",
    );
    fireEvent.click(screen.getByTestId("setup-preview"));
    expect(
      await screen.findByTestId("setup-items-Easypanel"),
    ).toHaveTextContent("Traefik routes");
    expect(screen.getByTestId("setup-sources-Easypanel")).toHaveTextContent(
      "1 of 2 services uploaded",
    );
    const call = fetchMock.mock.calls.find((c) =>
      String(c[0]).endsWith("setup/propose"),
    )!;
    expect(JSON.parse(String(call[1]!.body))).toMatchObject({
      scope: "easypanel_config",
    });
  });

  it("creates dump-only plans for the selected databases", async () => {
    const config = create(ConfigSchema, {
      repos: [create(RepoSchema, { id: "hosteurope", uri: "sftp:u@h:/b" })],
    });
    fetchMock.mockImplementation((url: string) =>
      Promise.resolve(
        json(
          url.endsWith("setup/propose")
            ? {
                ...proposals,
                mode: "databases",
                notes: ["budget: SQLite database data/db.sqlite is a file"],
                apps: [
                  {
                    ...proposals.apps[0],
                    plan: { ...proposals.apps[0].plan, plan_id: "app-shop-db" },
                  },
                ],
              }
            : url.endsWith("setup/create")
              ? [{ app: "compose:shop", plan_id: "app-shop-db" }]
              : {},
        ),
      ),
    );
    renderWithProviders(
      <SetupWizard apps={[]} easypanel={false} onDone={() => {}} />,
      { config },
    );
    fireEvent.change(screen.getByTestId("setup-scope"), {
      target: { value: "databases" },
    });
    fireEvent.click(screen.getByTestId("setup-preview"));
    const list = await screen.findByTestId("setup-proposals");
    expect(list).toHaveTextContent("app-shop-db");
    expect(list).toHaveTextContent("size known after the first dump");
    expect(list).toHaveTextContent("SQLite database");
    // Put the shop into a shared plan.
    fireEvent.change(screen.getByTestId("setup-new-group"), {
      target: { value: "Customers" },
    });
    fireEvent.click(screen.getByTestId("setup-add-group"));
    fireEvent.change(screen.getByTestId("setup-group-shop"), {
      target: { value: "customers" },
    });
    expect(screen.getByTestId("setup-groups")).toHaveTextContent(
      "customers: 1",
    );
    fireEvent.click(screen.getByTestId("setup-create"));
    await screen.findByTestId("setup-results");
    const call = fetchMock.mock.calls.find((c) =>
      String(c[0]).endsWith("setup/create"),
    )!;
    expect(JSON.parse(String(call[1]!.body))).toMatchObject({
      apps: [],
      mode: "databases",
      groups: { customers: ["compose:shop"] },
    });
  });
});
