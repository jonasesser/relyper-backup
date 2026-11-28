// relyper: tests for the Infrastructure page.
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithProviders } from "../../test/render";
import { ConfigSchema, RepoSchema } from "../../../gen/ts/v1/config_pb";
import { InfraPage } from "./InfraPage";
import type { CoverageReport } from "./infraApi";

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });

const coverage = (apps: CoverageReport["apps"]): CoverageReport => ({
  generated_at: "2026-11-27T10:00:00Z",
  docker: {
    available: true,
    endpoint: "unix:///var/run/docker.sock",
    version: "27.3.1",
    swarm: false,
    easypanel: true,
  },
  max_age_hours: 48,
  apps,
  totals: {},
});

const shop = {
  app: "compose:shop",
  name: "shop",
  kind: "compose",
  state: "unprotected",
  protection: "not_protected",
  actions: ["Create a backup plan for this app (it includes database dumps)."],
  databases: ["postgres"],
  items: [
    {
      service: "db",
      kind: "database" as const,
      name: "postgres",
      state: "unprotected",
    },
    {
      service: "web",
      kind: "data" as const,
      name: "uploads",
      path: "/srv/shop/uploads",
      state: "unprotected",
    },
  ],
};

describe("InfraPage", () => {
  let fetchMock: ReturnType<typeof vi.fn>;
  beforeEach(() => {
    fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("shows apps with their protection state", async () => {
    fetchMock.mockResolvedValue(json(coverage([shop])));
    renderWithProviders(<InfraPage />);
    const card = await screen.findByTestId("app-compose:shop");
    expect(card).toHaveTextContent("/srv/shop/uploads");
    expect(screen.getByTestId("infra-summary")).toHaveTextContent(
      "1 unprotected or failed",
    );
    expect(screen.getByTestId("infra-summary")).toHaveTextContent("Easypanel");
    expect(screen.getAllByTestId("state-badge")[0]).toHaveTextContent(
      /not protected/i,
    );
    expect(screen.getByTestId("app-actions")).toHaveTextContent(
      "Create a backup plan",
    );
  });

  it("shows whether Docker access goes through the guard", async () => {
    const rep = coverage([shop]);
    rep.docker.access = "unrestricted";
    fetchMock.mockResolvedValue(json(rep));
    const { unmount } = renderWithProviders(<InfraPage />);
    expect(await screen.findByTestId("infra-summary")).toHaveTextContent(
      "Unrestricted Docker access",
    );
    unmount();
    const guarded = coverage([shop]);
    guarded.docker.access = "guard";
    guarded.docker.guard_features = ["dumps", "restore_tests"];
    fetchMock.mockResolvedValue(json(guarded));
    renderWithProviders(<InfraPage />);
    await waitFor(() =>
      expect(screen.getByTestId("infra-summary")).toHaveTextContent(
        "Docker guard",
      ),
    );
    expect(screen.getByTestId("infra-summary")).not.toHaveTextContent(
      "Unrestricted",
    );
  });

  it("explains when Docker is not reachable", async () => {
    const rep = coverage(null);
    rep.docker = {
      available: false,
      endpoint: "unix:///var/run/docker.sock",
      swarm: false,
      easypanel: false,
      error: "no such file",
      hint: "No Docker guard is configured in this container",
    };
    fetchMock.mockResolvedValue(json(rep));
    renderWithProviders(<InfraPage />);
    expect(await screen.findByText("Docker not reachable")).toBeInTheDocument();
    expect(screen.getByTestId("docker-hint")).toHaveTextContent(
      "No Docker guard is configured",
    );
  });

  it("creates an app plan in the chosen repository", async () => {
    fetchMock.mockImplementation((url: string, init?: RequestInit) => {
      if (String(url).includes("plan/propose")) {
        return Promise.resolve(
          json({
            app: "compose:shop",
            plan_id: "app-compose-shop",
            paths: ["/data/relyper/dumps/compose-shop", "/srv/shop/uploads"],
            databases: ["db (postgres)"],
          }),
        );
      }
      if (String(url).includes("plan/create")) {
        expect(JSON.parse(String(init?.body))).toMatchObject({
          app: "compose:shop",
          repo: "local",
        });
        return Promise.resolve(
          json({ app: "compose:shop", plan_id: "app-compose-shop", paths: [] }),
        );
      }
      return Promise.resolve(json(coverage([shop])));
    });
    renderWithProviders(<InfraPage />, {
      config: create(ConfigSchema, {
        repos: [create(RepoSchema, { id: "local", uri: "/repo" })],
      }),
    });
    fireEvent.click(await screen.findByTestId("create-plan-toggle"));
    expect(
      await screen.findByText("/srv/shop/uploads", { exact: false }),
    ).toBeInTheDocument();
    fireEvent.click(await screen.findByTestId("create-plan-submit"));
    await waitFor(() =>
      expect(
        fetchMock.mock.calls.some(([u]) => String(u).includes("plan/create")),
      ).toBe(true),
    );
  });

  it("filters apps by status and database", async () => {
    const blog = {
      ...shop,
      app: "easypanel:blog",
      name: "blog",
      kind: "easypanel",
      protection: "fully_protected",
      actions: [],
      databases: ["mysql"],
    };
    fetchMock.mockImplementation(() =>
      Promise.resolve(json(coverage([shop, blog]))),
    );
    renderWithProviders(<InfraPage />);
    await screen.findByTestId("app-compose:shop");
    fireEvent.change(screen.getByTestId("filter-protection"), {
      target: { value: "fully_protected" },
    });
    expect(screen.queryByTestId("app-compose:shop")).toBeNull();
    expect(screen.getByTestId("app-easypanel:blog")).toBeInTheDocument();
    fireEvent.change(screen.getByTestId("filter-db"), {
      target: { value: "postgres" },
    });
    expect(screen.getByText("No app matches the filter.")).toBeInTheDocument();
  });
});
