// relyper: tests for the Infrastructure page.
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithProviders } from "../../test/render";
import { ConfigSchema, RepoSchema } from "../../../gen/ts/v1/config_pb";
import { InfraPage } from "./InfraPage";
import type { CoverageReport } from "./infraApi";

// jsdom has no PointerEvent; the checkbox code creates one on blur.
if (typeof window.PointerEvent === "undefined") {
  (window as any).PointerEvent = MouseEvent;
}

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

  it("searches services, images and domains, not only app names", async () => {
    const blog = { ...shop, app: "compose:blog", name: "blog", items: [] };
    fetchMock.mockImplementation((url: string) => {
      if (String(url).includes("inventory")) {
        return Promise.resolve(
          json({
            apps: [
              {
                id: "compose:shop",
                services: [
                  {
                    name: "web",
                    image: "ghcr.io/acme/shopfront:2",
                    state: "running",
                    domains: ["store.example.com"],
                  },
                ],
              },
              { id: "compose:blog", services: [] },
            ],
          }),
        );
      }
      return Promise.resolve(json(coverage([shop, blog])));
    });
    renderWithProviders(<InfraPage />);
    await screen.findByTestId("app-compose:blog");
    const search = screen.getByLabelText("Search apps");
    fireEvent.change(search, { target: { value: "STORE.example" } });
    await waitFor(() =>
      expect(screen.queryByTestId("app-compose:blog")).toBeNull(),
    );
    expect(screen.getByTestId("search-matches")).toHaveTextContent(
      "domain store.example.com",
    );
    expect(screen.getByTestId("service-details")).toBeInTheDocument();
    fireEvent.change(search, { target: { value: "uploads" } });
    expect(screen.getByTestId("app-compose:shop")).toBeInTheDocument();
    expect(
      screen.getByTestId("app-compose:shop").querySelector("[data-match]"),
    ).not.toBeNull();
  });

  it("lets the user choose the plan items", async () => {
    let sent: any = null;
    fetchMock.mockImplementation((url: string, init?: RequestInit) => {
      if (String(url).includes("plan/propose")) {
        return Promise.resolve(
          json({
            app: "compose:shop",
            plan_id: "app-shop",
            paths: ["/code", "/v/up", "/inv"],
            items: [
              {
                path: "/code",
                kind: "config",
                label: "compose directory",
                selected: true,
              },
              {
                path: "/v/up",
                kind: "data",
                service: "web",
                label: "volume up",
                selected: true,
              },
              {
                path: "/code/tmp",
                kind: "data",
                service: "web",
                label: "bind tmp",
                class: "temporary",
                selected: false,
                note: "temporary data",
              },
              {
                path: "/inv",
                kind: "inventory",
                label: "recovery report",
                selected: true,
                required: true,
              },
            ],
          }),
        );
      }
      if (String(url).includes("plan/create")) {
        sent = JSON.parse(String(init?.body));
        return Promise.resolve(
          json({ app: "compose:shop", plan_id: "app-shop", paths: [] }),
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
    const items = await screen.findByTestId("plan-items");
    expect(items).toHaveTextContent("temporary data");
    // untick the volume, tick the tmp bind
    const box = (p: string) =>
      screen
        .getByTestId(`plan-item-${p}`)
        .querySelector("[data-part=control]")!;
    const user = userEvent.setup();
    await user.click(box("/v/up"));
    await user.click(box("/code/tmp"));
    fireEvent.click(screen.getByTestId("create-plan-submit"));
    await waitFor(() => expect(sent).not.toBeNull());
    expect(sent.paths).toEqual(["/code", "/code/tmp", "/inv"]);
  });

  it("links to adding a repository when there is none", async () => {
    fetchMock.mockImplementation((url: string) => {
      if (String(url).includes("plan/propose")) {
        return Promise.resolve(
          json({ app: "compose:shop", plan_id: "p", paths: ["/a"] }),
        );
      }
      return Promise.resolve(json(coverage([shop])));
    });
    renderWithProviders(<InfraPage />);
    fireEvent.click(await screen.findByTestId("create-plan-toggle"));
    expect(await screen.findByTestId("add-repo-link")).toHaveTextContent(
      "Add a repository",
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
      target: { value: "group:protected" },
    });
    expect(screen.queryByTestId("app-compose:shop")).toBeNull();
    expect(screen.getByTestId("app-easypanel:blog")).toBeInTheDocument();
    fireEvent.change(screen.getByTestId("filter-db"), {
      target: { value: "postgres" },
    });
    expect(screen.getByText("No app matches the filter.")).toBeInTheDocument();
  });

  it("summary badges filter by the same groups as the status filter", async () => {
    const blog = {
      ...shop,
      app: "easypanel:blog",
      name: "blog",
      kind: "easypanel",
      protection: "backup_overdue",
      actions: [],
    };
    fetchMock.mockImplementation(() =>
      Promise.resolve(json(coverage([shop, blog]))),
    );
    renderWithProviders(<InfraPage />);
    await screen.findByTestId("app-compose:shop");
    const attention = screen.getByTestId("summary-attention");
    expect(attention).toHaveTextContent("1 need attention");
    fireEvent.click(attention);
    expect(screen.queryByTestId("app-compose:shop")).toBeNull();
    expect(screen.getByTestId("app-easypanel:blog")).toBeInTheDocument();
    expect(screen.getByTestId("filter-protection")).toHaveValue(
      "group:attention",
    );
    fireEvent.click(screen.getByTestId("summary-attention"));
    expect(screen.getByTestId("app-compose:shop")).toBeInTheDocument();
    fireEvent.click(screen.getByTestId("summary-unprotected"));
    expect(screen.getByTestId("app-compose:shop")).toBeInTheDocument();
    expect(screen.queryByTestId("app-easypanel:blog")).toBeNull();
  });

  it("shows Easypanel projects, tags and tag groups", async () => {
    let sent: any = null;
    const web = {
      ...shop,
      app: "easypanel:a_web",
      name: "web",
      kind: "easypanel",
      project: "a",
      tags: ["shop"],
    };
    const blog = {
      ...shop,
      app: "easypanel:b_blog",
      name: "blog",
      kind: "easypanel",
      project: "b",
    };
    const group = {
      ...shop,
      app: "group:shop",
      name: "shop",
      kind: "group",
      tags: ["shop"],
      members: ["easypanel:a_web"],
    };
    fetchMock.mockImplementation((url: string, init?: RequestInit) => {
      if (String(url).includes("tags/set")) {
        sent = JSON.parse(String(init?.body));
        return Promise.resolve(json({ app: sent.app, tags: ["shop", "x"] }));
      }
      return Promise.resolve(json(coverage([web, blog, group])));
    });
    renderWithProviders(<InfraPage />);
    const card = await screen.findByTestId("app-easypanel:a_web");
    expect(
      screen.getByText("Easypanel project a · easypanel:a_web"),
    ).toBeInTheDocument();
    expect(card).toHaveTextContent("# shop");
    // The group is listed on its own and not counted with the apps.
    expect(screen.getByTestId("tag-groups")).toHaveTextContent(
      "Tag group · 1 apps",
    );
    expect(screen.getByTestId("summary-unprotected")).toHaveTextContent(
      "2 unprotected",
    );
    fireEvent.change(screen.getByTestId("filter-tag"), {
      target: { value: "shop" },
    });
    expect(screen.queryByTestId("app-easypanel:b_blog")).toBeNull();
    expect(screen.getByTestId("app-group:shop")).toBeInTheDocument();
    fireEvent.change(screen.getByTestId("filter-tag"), {
      target: { value: "" },
    });
    fireEvent.change(screen.getByLabelText("Search apps"), {
      target: { value: "project b" },
    });
    expect(screen.queryByTestId("app-easypanel:a_web")).toBeNull();
    expect(screen.getByTestId("app-easypanel:b_blog")).toHaveTextContent(
      "Easypanel project b",
    );
    fireEvent.change(screen.getByLabelText("Search apps"), {
      target: { value: "" },
    });
    const blogCard = screen.getByTestId("app-easypanel:b_blog");
    fireEvent.click(blogCard.querySelector("[data-testid=tags-toggle]")!);
    fireEvent.change(screen.getByLabelText("Tags"), {
      target: { value: "shop, x" },
    });
    fireEvent.click(screen.getByTestId("tag-save"));
    await waitFor(() => expect(sent).not.toBeNull());
    expect(sent).toEqual({ app: "easypanel:b_blog", tags: ["shop", "x"] });
  });

  it("shows linked databases and hides old Swarm tasks behind a toggle", async () => {
    const budget = {
      ...shop,
      app: "easypanel:hosting_budget",
      name: "budget",
      kind: "easypanel",
      project: "hosting",
      databases: [],
      items: [
        {
          service: "budget",
          kind: "data" as const,
          name: "budget_data",
          path: "/v/budget",
          state: "unprotected",
          sqlite: ["server-files/account.sqlite"],
        },
        {
          service: "",
          kind: "app_definition" as const,
          name: "app definition",
          path: "/dumps/_inventory/apps/budget.json",
          state: "unprotected",
        },
      ],
      database_links: [
        {
          from: "budget",
          engine: "postgres",
          host: "hosting_db",
          database: "budget",
          var: "DATABASE_URL",
          app: "easypanel:hosting_db",
          service: "db",
        },
      ],
      old_tasks: [
        {
          service: "budget",
          container: "hosting_budget.1.old",
          image: "sha256:abc",
          state: "exited",
        },
      ],
    };
    const db = {
      ...shop,
      app: "easypanel:hosting_db",
      name: "db",
      kind: "easypanel",
      project: "hosting",
      protection: "fully_protected",
    };
    fetchMock.mockResolvedValue(json(coverage([budget, db])));
    renderWithProviders(<InfraPage />);
    const card = await screen.findByTestId("app-easypanel:hosting_budget");
    const dbs = card.querySelector("[data-testid=db-summary]")!;
    expect(dbs).toHaveTextContent(
      'budget uses postgres database "budget" on db',
    );
    expect(dbs).toHaveTextContent("which runs in app db");
    expect(dbs).toHaveTextContent(
      "SQLite in volume budget_data: server-files/account.sqlite",
    );
    expect(card).toHaveTextContent("configuration app definition");
    const old = card.querySelector("[data-testid=old-tasks]")!;
    expect(old).toHaveTextContent(
      "1 stopped container from earlier deployments",
    );
    expect(old).not.toHaveTextContent("hosting_budget.1.old");
    fireEvent.click(card.querySelector("[data-testid=old-tasks-toggle]")!);
    expect(old).toHaveTextContent("hosting_budget.1.old");
    // An app with its own database lists it.
    const shopCard = screen.getByTestId("app-easypanel:hosting_db");
    expect(
      shopCard.querySelector("[data-testid=db-summary]"),
    ).toHaveTextContent("postgres in service db");
  });

  it("takes the status filter from the link", async () => {
    const blog = {
      ...shop,
      app: "compose:blog",
      name: "blog",
      protection: "fully_protected",
      actions: [],
    };
    fetchMock.mockResolvedValue(json(coverage([shop, blog])));
    renderWithProviders(<InfraPage />, {
      route: "/?status=group:unprotected",
    });
    await screen.findByTestId("app-compose:shop");
    expect(screen.queryByTestId("app-compose:blog")).toBeNull();
    expect(screen.getByTestId("filter-protection")).toHaveValue(
      "group:unprotected",
    );
  });

  it("opens the disaster recovery guide from the link", async () => {
    fetchMock.mockImplementation((url: string) =>
      Promise.resolve(
        String(url).includes("report")
          ? new Response(
              "# Disaster recovery report\n\n## If the whole server is lost\n\n1. **New server**",
              { status: 200 },
            )
          : json(coverage([shop])),
      ),
    );
    renderWithProviders(<InfraPage />, { route: "/?tab=report" });
    expect(
      await screen.findByRole("heading", {
        name: "If the whole server is lost",
      }),
    ).toBeInTheDocument();
    expect(screen.getByTestId("report-download")).toBeInTheDocument();
  });
});
