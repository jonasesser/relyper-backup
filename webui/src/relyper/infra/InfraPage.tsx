// relyper: Infrastructure page – apps on the Docker host, what of them is
// protected by a completed backup, app backup plans with database dumps,
// restore tests, recovery readiness and the disaster recovery report.
import React, { useCallback, useEffect, useState } from "react";
import {
  Badge,
  Box,
  Code,
  Flex,
  Heading,
  Input,
  NativeSelect,
  Stack,
  Text,
} from "@chakra-ui/react";
import { Button } from "../../components/ui/button";
import { Checkbox } from "../../components/ui/checkbox";
import { Field } from "../../components/ui/field";
import { SectionCard } from "../../components/common/SectionCard";
import {
  TabsContent,
  TabsList,
  TabsRoot,
  TabsTrigger,
} from "../../components/ui/tabs";
import { alerts } from "../../components/common/Alerts";
import { useConfig } from "../../app/provider";
import {
  AppCoverage,
  AppPlan,
  DataItem,
  CoverageReport,
  ReadinessReport,
  Results,
  ServiceInfo,
  infraApi,
} from "./infraApi";
import { useLocation } from "react-router";
import { SetupWizard } from "./SetupWizard";
import { RecoveryPanel } from "./RecoveryPanel";
import { AddRepoLink } from "./AddRepoLink";

export const stateColor: Record<string, string> = {
  protected: "green",
  covered_by_dump: "green",
  ready: "green",
  ok: "green",
  remote: "gray",
  stopped: "gray",
  no_data: "gray",
  not_needed: "gray",
  unsupported: "orange",
  fully_protected: "green",
  partially_protected: "orange",
  not_protected: "red",
  backup_failed: "red",
  backup_overdue: "orange",
  restore_not_verified: "yellow",
  unsupported_configuration: "orange",
  stale: "orange",
  not_in_snapshot: "orange",
  disabled: "orange",
  at_risk: "orange",
  warning: "orange",
  failing: "red",
  not_reachable: "red",
  unprotected: "red",
  not_ready: "red",
  failed: "red",
};

const stateLabel: Record<string, string> = {
  protected: "protected",
  covered_by_dump: "via dump",
  not_in_snapshot: "not in a snapshot yet",
  not_reachable: "not reachable",
  at_risk: "at risk",
  not_ready: "not ready",
  no_data: "no data",
  not_needed: "not needed",
  remote: "other node",
};

// Status groups shown as summary badges. Each badge filters the app list
// by its group, and the status filter offers the same groups, so the
// numbers on the badges and in the filter always match.
export const protectionGroups: {
  id: string;
  label: string;
  color: string;
  statuses: string[];
}[] = [
  {
    id: "protected",
    label: "fully protected",
    color: "green",
    statuses: ["fully_protected"],
  },
  {
    id: "attention",
    label: "need attention",
    color: "orange",
    statuses: [
      "restore_not_verified",
      "partially_protected",
      "backup_overdue",
      "unsupported_configuration",
    ],
  },
  {
    id: "unprotected",
    label: "unprotected or failed",
    color: "red",
    statuses: ["not_protected", "backup_failed"],
  },
  { id: "nodata", label: "no data", color: "gray", statuses: ["no_data"] },
];

// matchesStatus applies a status filter value: "" (all), "group:<id>" or a
// single status.
export const matchesStatus = (filter: string, protection: string) => {
  if (!filter) return true;
  if (filter.startsWith("group:")) {
    const g = protectionGroups.find((g) => "group:" + g.id === filter);
    return !!g && g.statuses.includes(protection);
  }
  return protection === filter;
};

const humanMB = (b?: number) =>
  b ? `${Math.round(b / (1 << 20)).toLocaleString()} MB` : "";

const ServiceDetails = ({ services }: { services: ServiceInfo[] }) => (
  <Stack gap={2} fontSize="xs" data-testid="service-details">
    {services.map((s) => (
      <Box key={s.name} borderTopWidth="1px" pt={2}>
        <Text fontWeight="medium" fontSize="sm">
          {s.name}{" "}
          <Text as="span" color="fg.muted" fontWeight="normal">
            {s.image} · {s.state}
          </Text>
        </Text>
        <Flex columnGap={3} rowGap={0} wrap="wrap" color="fg.muted">
          {s.database && <Text>database {s.database.engine}</Text>}
          {s.unsupported_database && (
            <Text>database {s.unsupported_database} (no dump support)</Text>
          )}
          {s.restart_policy && <Text>restart {s.restart_policy}</Text>}
          {s.memory_limit_bytes ? (
            <Text>memory {humanMB(s.memory_limit_bytes)}</Text>
          ) : null}
          {s.cpu_limit ? <Text>cpu {s.cpu_limit}</Text> : null}
          {(s.networks || []).length > 0 && (
            <Text>networks {s.networks!.join(", ")}</Text>
          )}
          {(s.ports || []).length > 0 && (
            <Text>ports {s.ports!.join(", ")}</Text>
          )}
          {(s.depends_on || []).length > 0 && (
            <Text>depends on {s.depends_on!.join(", ")}</Text>
          )}
          {(s.domains || []).length > 0 && (
            <Text wordBreak="break-all">domains {s.domains!.join(", ")}</Text>
          )}
        </Flex>
      </Box>
    ))}
  </Stack>
);

// Anonymous volumes carry a 64-hex name; bind mounts repeat their path.
// kindLabel names the kind of a coverage item; settings of every kind are
// "configuration", the item name says which.
export const kindLabel = (kind: DataItem["kind"]) => {
  switch (kind) {
    case "deployment":
      return "deployment definition";
    case "service_folder":
    case "easypanel_settings":
    case "app_definition":
      return "configuration";
    default:
      return kind.replace("_", " ");
  }
};

export const itemName = (it: DataItem) => {
  if (it.name === it.path) return "";
  if (/^[0-9a-f]{64}$/.test(it.name))
    return `anonymous volume ${it.name.slice(0, 12)}`;
  return it.name;
};

// searchMatches lists where a search text occurs in an app: its name, its
// services (name, container, image, Swarm service, domains, ports,
// networks) and its data items (volume names, paths). Empty when nothing
// matches; case-insensitive substring search.
export const searchMatches = (
  app: AppCoverage,
  services: ServiceInfo[],
  text: string,
): string[] => {
  const q = text.trim().toLowerCase();
  if (!q) return [];
  const out: string[] = [];
  const hit = (v: string | undefined, label: string) => {
    if (v && v.toLowerCase().includes(q) && !out.includes(label))
      out.push(label);
  };
  hit(app.name, "app " + app.name);
  hit(app.app, "app " + app.app);
  // "project b" finds the services of Easypanel project b.
  if (app.project)
    hit("Easypanel project " + app.project, "Easypanel project " + app.project);
  for (const t of app.tags || []) hit(t, "tag " + t);
  for (const s of services) {
    hit(s.name, "service " + s.name);
    hit(s.container, "container " + s.container);
    hit(s.swarm_service, "service " + s.swarm_service);
    hit(s.image, "image " + s.image);
    for (const d of s.domains || []) hit(d, "domain " + d);
    for (const p of s.ports || []) hit(p, "port " + p);
    for (const n of s.networks || []) hit(n, "network " + n);
  }
  for (const it of app.items || []) {
    hit(it.service, "service " + it.service);
    hit(it.name, `${it.kind.replace("_", " ")} ${it.name}`);
    hit(it.path, "path " + it.path);
  }
  return out;
};

// itemMatches tells whether a data item row matches the search text.
const itemMatches = (it: DataItem, text: string) => {
  const q = text.trim().toLowerCase();
  return (
    !!q &&
    [it.service, it.name, it.path].some((v) => v && v.toLowerCase().includes(q))
  );
};

export const StateBadge = ({ state }: { state: string }) => (
  <Badge colorPalette={stateColor[state] || "gray"} data-testid="state-badge">
    {stateLabel[state] || state.replace(/_/g, " ")}
  </Badge>
);

const fmt = (s?: string) => (s ? new Date(s).toLocaleString() : "–");

const errMsg = (e: unknown) => (e instanceof Error ? e.message : String(e));

const CreatePlanForm = ({
  app,
  onDone,
}: {
  app: AppCoverage;
  onDone: () => void;
}) => {
  const [config] = useConfig();
  const repos = (config?.repos || []).filter((r) => !r.originInstanceId);
  const [proposal, setProposal] = useState<AppPlan | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [repo, setRepo] = useState("");
  const [planId, setPlanId] = useState("");
  const [busy, setBusy] = useState(false);
  const [chosen, setChosen] = useState<Record<string, boolean>>({});

  useEffect(() => {
    infraApi
      .propose(app.app)
      .then((p) => {
        setProposal(p);
        setPlanId(p.plan_id);
        const c: Record<string, boolean> = {};
        for (const it of p.items || []) c[it.path] = it.selected;
        setChosen(c);
      })
      .catch((e) => setError(errMsg(e)));
  }, [app.app]);

  useEffect(() => {
    if (!repo && repos.length > 0) setRepo(repos[0].id);
  }, [repos, repo]);

  if (error) {
    return (
      <Text color="red.500" fontSize="sm">
        {error}
      </Text>
    );
  }
  if (!proposal) return <Text fontSize="sm">Loading proposal…</Text>;

  const create = async () => {
    setBusy(true);
    try {
      const items = proposal.items || [];
      await infraApi.createPlan(
        app.app,
        repo,
        planId,
        items.length
          ? items.filter((i) => i.required || chosen[i.path]).map((i) => i.path)
          : undefined,
      );
      alerts.success(
        `Plan ${planId} created. Run a first backup from the plan page.`,
      );
      onDone();
    } catch (e) {
      alerts.error(errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Stack gap={2} fontSize="sm" data-testid="create-plan-form">
      <Text fontWeight="medium">This plan will back up:</Text>
      {proposal.items && proposal.items.length > 0 ? (
        <Stack gap={2} data-testid="plan-items">
          {proposal.items.map((it) => (
            <Checkbox
              key={it.path}
              size="sm"
              alignItems="flex-start"
              checked={it.required || !!chosen[it.path]}
              disabled={it.required}
              onCheckedChange={(e) =>
                setChosen({ ...chosen, [it.path]: !!e.checked })
              }
              data-testid={`plan-item-${it.path}`}
            >
              <Box minW="0">
                <Text wordBreak="break-word">
                  {it.service ? `${it.service} · ` : ""}
                  {it.label}
                  {it.required ? " (always)" : ""}
                </Text>
                <Code fontSize="xs" wordBreak="break-all">
                  {it.path}
                </Code>
                {it.note && (
                  <Text fontSize="xs" color="fg.muted">
                    {it.note}
                  </Text>
                )}
              </Box>
            </Checkbox>
          ))}
          <Text fontSize="xs" color="fg.muted">
            Unchecked items inside a checked directory are excluded. You can
            change paths and excludes later on the plan page.
          </Text>
        </Stack>
      ) : (
        <Stack gap={1}>
          {proposal.paths.map((p) => (
            <Code key={p} fontSize="xs" wordBreak="break-all">
              {p}
            </Code>
          ))}
        </Stack>
      )}
      {proposal.databases && proposal.databases.length > 0 && (
        <Text>
          Databases dumped right before every backup:{" "}
          {proposal.databases.join(", ")}. A failed dump fails the backup.
        </Text>
      )}
      {(proposal.skipped || []).length > 0 && (
        <Box
          borderWidth="1px"
          borderColor="orange.400"
          bg="orange.subtle"
          rounded="md"
          p={2}
          data-testid="plan-missing"
        >
          <Text fontWeight="medium">Missing from this plan</Text>
          <Stack as="ul" gap={0.5} pl={5} listStyleType="disc">
            {proposal.skipped!.map((s) => (
              <li key={s}>{s}</li>
            ))}
          </Stack>
        </Box>
      )}
      {(proposal.warnings || []).map((s) => (
        <Text key={s} color="orange.500">
          {s}
        </Text>
      ))}
      {repos.length === 0 ? (
        <AddRepoLink />
      ) : (
        <Flex gap={3} wrap="wrap" align="flex-end">
          <Field label="Repository" w="220px">
            <NativeSelect.Root size="sm">
              <NativeSelect.Field
                value={repo}
                onChange={(e) => setRepo(e.target.value)}
                data-testid="create-plan-repo"
              >
                {repos.map((r) => (
                  <option key={r.id} value={r.id}>
                    {r.id}
                  </option>
                ))}
              </NativeSelect.Field>
              <NativeSelect.Indicator />
            </NativeSelect.Root>
          </Field>
          <Field label="Plan ID" w="220px">
            <Input
              size="sm"
              value={planId}
              onChange={(e) => setPlanId(e.target.value)}
            />
          </Field>
          <Button
            size="sm"
            colorPalette="blue"
            loading={busy}
            onClick={create}
            disabled={!repo || !planId}
            data-testid="create-plan-submit"
          >
            Create plan
          </Button>
        </Flex>
      )}
      <Text color="fg.muted" fontSize="xs">
        Schedule daily at 02:30, keep 7 daily, 4 weekly and 6 monthly snapshots.
        You can change both on the plan page afterwards.
      </Text>
    </Stack>
  );
};

const TestResults = ({ app }: { app: AppCoverage }) => {
  const [res, setRes] = useState<Results | null>(null);
  const plan = (app.plans || [])[0] || "";
  const load = useCallback(() => {
    infraApi
      .results(app.app, plan)
      .then(setRes)
      .catch(() => setRes(null));
  }, [app.app, plan]);
  useEffect(() => {
    load();
    const t = setInterval(load, 5000);
    return () => clearInterval(t);
  }, [load]);
  const db = res?.database_restore_tests?.[0];
  const ft = res?.file_restore_tests?.[0];
  return (
    <Stack gap={1} fontSize="sm">
      <Flex gap={2} align="center" wrap="wrap">
        <Text>Database restore test:</Text>
        {db ? (
          <>
            <StateBadge state={db.ok ? "ok" : "failed"} />
            <Text color="fg.muted">
              {fmt(db.time)}, snapshot {db.snapshot_id.slice(0, 8)}
            </Text>
          </>
        ) : (
          <Text color="fg.muted">none yet</Text>
        )}
      </Flex>
      {db && !db.ok && (
        <Text color="red.500" fontSize="xs" wordBreak="break-word">
          {db.error ||
            (db.results || [])
              .flatMap((r) =>
                (r.problems || []).map((p) => r.service + ": " + p),
              )
              .join("; ")}
        </Text>
      )}
      {plan && (
        <Flex gap={2} align="center" wrap="wrap">
          <Text>File restore test ({plan}):</Text>
          {ft ? (
            <>
              <StateBadge state={ft.ok ? "ok" : "failed"} />
              <Text color="fg.muted">
                {fmt(ft.time)}, {(ft.files || []).length} files
              </Text>
            </>
          ) : (
            <Text color="fg.muted">none yet</Text>
          )}
        </Flex>
      )}
      {ft && !ft.ok && (
        <Text color="red.500" fontSize="xs">
          {ft.error}
        </Text>
      )}
    </Stack>
  );
};

// appDescription is the line under an app's name: its ID and type, and for
// an Easypanel service the Easypanel project it belongs to.
export const appDescription = (app: AppCoverage) => {
  if (app.kind === "group")
    return `Tag group · ${(app.members || []).length} apps`;
  const kind = app.kind.replace("_", " ");
  return app.project
    ? `Easypanel project ${app.project} · ${app.app}`
    : `${app.app} · ${kind}`;
};

// TagEditor edits the Relyper Backup tags of an app. Tags live only in
// Relyper Backup (next to its config) and group apps across Easypanel
// projects; every tag can get its own backup plan.
const TagEditor = ({
  app,
  onDone,
}: {
  app: AppCoverage;
  onDone: () => void;
}) => {
  const [text, setText] = useState((app.tags || []).join(", "));
  const [busy, setBusy] = useState(false);
  const save = async () => {
    setBusy(true);
    try {
      const tags = text
        .split(",")
        .map((t) => t.trim())
        .filter(Boolean);
      await infraApi.setTags(app.app, tags);
      alerts.success("Tags saved");
      onDone();
    } catch (e) {
      alerts.error(errMsg(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Stack gap={1} fontSize="sm" data-testid="tag-editor">
      <Flex gap={2} wrap="wrap">
        <Input
          size="sm"
          flex="1 1 220px"
          aria-label="Tags"
          placeholder="e.g. shop, customer-a"
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && save()}
        />
        <Button size="sm" loading={busy} onClick={save} data-testid="tag-save">
          Save tags
        </Button>
      </Flex>
      <Text fontSize="xs" color="fg.muted">
        Separate tags with commas. Apps with the same tag form a tag group that
        can get one backup plan, also across Easypanel projects. Tags exist only
        in Relyper Backup.
      </Text>
    </Stack>
  );
};

// DatabaseSummary says plainly which databases an app has or uses and how
// they are protected.
export const DatabaseSummary = ({
  app,
  appsById,
}: {
  app: AppCoverage;
  appsById: Record<string, AppCoverage>;
}) => {
  const own = (app.items || []).filter((i) => i.kind === "database");
  const links = app.database_links || [];
  const sqlite = (app.items || []).filter((i) => (i.sqlite || []).length > 0);
  return (
    <Box fontSize="sm" data-testid="db-summary">
      <Text fontWeight="medium">Databases</Text>
      {own.length === 0 && links.length === 0 && sqlite.length === 0 && (
        <Text color="fg.muted">
          No database found in this app (no database container and no database
          connection in its environment).
        </Text>
      )}
      <Stack as="ul" gap={0.5} pl={5} listStyleType="disc">
        {own.map((it, i) => (
          <li key={"o" + i}>
            {it.name} in service <b>{it.service}</b>
            {it.state === "unsupported"
              ? ": no dump support, files only"
              : it.plan
                ? `: dumped by plan ${it.plan}`
                : ": dumped once the app has a backup plan"}
          </li>
        ))}
        {sqlite.map((it) => (
          <li key={"s" + it.path}>
            SQLite in volume <b>{itemName(it) || it.path}</b>:{" "}
            {it.sqlite!.join(", ")}. Backed up as files with the volume; a
            backup taken during a write may need SQLite's own recovery when
            restored.
          </li>
        ))}
        {links.map((l, i) => {
          const target = l.app ? appsById[l.app] : undefined;
          const what = `${l.from} uses ${l.engine}${
            l.database && l.engine !== "sqlite"
              ? ` database "${l.database}"`
              : ""
          }`;
          if (l.engine === "sqlite")
            return (
              <li key={"l" + i}>
                {l.from} uses an SQLite file <Code>{l.database}</Code>: it is
                backed up with the volume that holds it.
              </li>
            );
          return (
            <li key={"l" + i}>
              {what} on <b>{l.service || l.host}</b>
              {target ? (
                <>
                  , which runs in app <b>{target.name}</b>. Its dump comes from
                  that app's backup: <StateBadge state={target.protection} />
                </>
              ) : (
                ` (${l.host}, not on this host: back it up where it runs)`
              )}
            </li>
          );
        })}
      </Stack>
    </Box>
  );
};

const AppCard = ({
  app,
  services,
  reload,
  search = "",
  appsById = {},
}: {
  app: AppCoverage;
  services: ServiceInfo[];
  reload: () => void;
  search?: string;
  appsById?: Record<string, AppCoverage>;
}) => {
  const [showCreate, setShowCreate] = useState(false);
  const [showRecovery, setShowRecovery] = useState(false);
  const [showDetails, setShowDetails] = useState(false);
  const [showTags, setShowTags] = useState(false);
  const [busy, setBusy] = useState<string | null>(null);
  const isGroup = app.kind === "group";
  // The server sends null for apps without data items.
  const items = app.items || [];
  const hasDB = items.some((i) => i.kind === "database");
  const hasAppPlan = items.some((i) => i.kind === "database" && i.plan);
  const plan = (app.plans || [])[0];
  // Search hits other than the app's own name are listed, and services are
  // shown when the hit is in a service (image, domain, container, …).
  // When the app's own name matches, the other hits add nothing.
  const allMatches = searchMatches(app, services, search);
  const matches = allMatches.some(
    (m) => m === "app " + app.name || m === "app " + app.app,
  )
    ? []
    : allMatches;
  const [showHistory, setShowHistory] = useState(false);
  const oldTasks = app.old_tasks || [];
  const q = search.trim().toLowerCase();
  const serviceHit =
    !!q &&
    services.some((sv) =>
      [
        sv.name,
        sv.container,
        sv.swarm_service,
        sv.image,
        ...(sv.domains || []),
        ...(sv.ports || []),
        ...(sv.networks || []),
      ].some((v) => v && v.toLowerCase().includes(q)),
    );

  const act = async (name: string, fn: () => Promise<unknown>, ok: string) => {
    setBusy(name);
    try {
      await fn();
      alerts.success(ok);
      reload();
    } catch (e) {
      alerts.error(errMsg(e));
    } finally {
      setBusy(null);
    }
  };

  return (
    <SectionCard title={app.name} description={appDescription(app)}>
      <Stack gap={3} data-testid={`app-${app.app}`}>
        <Flex gap={2} align="center" wrap="wrap">
          <StateBadge state={app.protection || app.state} />
          {(app.plans || []).map((p) => (
            <Badge key={p} variant="outline">
              plan {p}
            </Badge>
          ))}
          {!isGroup &&
            (app.tags || []).map((t) => (
              <Badge
                key={t}
                colorPalette="purple"
                variant="subtle"
                data-testid="tag-badge"
              >
                # {t}
              </Badge>
            ))}
        </Flex>
        {isGroup && (app.members || []).length > 0 && (
          <Text fontSize="xs" color="fg.muted" data-testid="group-members">
            Apps: {app.members!.join(", ")}
          </Text>
        )}
        {matches.length > 0 && (
          <Text fontSize="xs" data-testid="search-matches">
            <Text as="span" color="fg.muted">
              Matches:{" "}
            </Text>
            {matches.slice(0, 4).join(" · ")}
            {matches.length > 4 && ` · and ${matches.length - 4} more`}
          </Text>
        )}
        <Stack gap={0} fontSize="sm">
          {items.map((it, i) => (
            <Flex
              key={i}
              gap={2}
              py={1.5}
              px={itemMatches(it, search) ? 1 : 0}
              bg={itemMatches(it, search) ? "yellow.subtle" : undefined}
              data-match={itemMatches(it, search) || undefined}
              borderTopWidth={i ? "1px" : 0}
              wrap="wrap"
              align="center"
            >
              <Box minW="0" flex="1 1 240px">
                <Text fontWeight="medium" wordBreak="break-word">
                  {it.service ? `${it.service} · ` : ""}
                  {kindLabel(it.kind)}{" "}
                  <Text as="span" color="fg.muted" fontWeight="normal">
                    {itemName(it)}
                  </Text>
                </Text>
                {it.path && (
                  <Text fontSize="xs" color="fg.muted" wordBreak="break-all">
                    {it.path}
                  </Text>
                )}
                {it.detail && (
                  <Text fontSize="xs" color="fg.muted" wordBreak="break-word">
                    {it.detail}
                  </Text>
                )}
              </Box>
              <Flex gap={2} align="center" wrap="wrap">
                <StateBadge state={it.state} />
                {it.last_snapshot && (
                  <Text fontSize="xs" color="fg.muted">
                    {fmt(it.last_snapshot)}
                  </Text>
                )}
              </Flex>
            </Flex>
          ))}
          {items.length === 0 && (
            <Text color="fg.muted">Nothing to back up.</Text>
          )}
        </Stack>
        {!isGroup && <DatabaseSummary app={app} appsById={appsById} />}
        {(app.notes || []).map((n) => (
          <Text key={n} fontSize="xs" color="fg.muted">
            {n}
          </Text>
        ))}
        {(app.actions || []).length > 0 && (
          <Box fontSize="sm" data-testid="app-actions">
            <Text fontWeight="medium">What to do</Text>
            <Stack as="ol" gap={0.5} pl={5} listStyleType="decimal">
              {app.actions!.map((a) => (
                <li key={a}>{a}</li>
              ))}
            </Stack>
          </Box>
        )}
        {(showDetails || serviceHit) && services.length > 0 && (
          <ServiceDetails services={services} />
        )}
        {oldTasks.length > 0 && (
          <Box fontSize="xs" color="fg.muted" data-testid="old-tasks">
            <Text>
              {oldTasks.length} stopped{" "}
              {oldTasks.length === 1 ? "container" : "containers"} from earlier
              deployments (kept by Swarm, not backed up; the data belongs to the
              service).{" "}
              <Button
                size="xs"
                variant="plain"
                px={0}
                h="auto"
                textDecoration="underline"
                onClick={() => setShowHistory(!showHistory)}
                data-testid="old-tasks-toggle"
              >
                {showHistory ? "Hide" : "Show"}
              </Button>
            </Text>
            {showHistory && (
              <Stack as="ul" gap={0} pl={4} mt={1} listStyleType="disc">
                {oldTasks.map((t) => (
                  <li key={t.container}>
                    <Text as="span" wordBreak="break-all">
                      {t.container} · {t.image.slice(0, 40)} ·{" "}
                      {t.status || t.state}
                    </Text>
                  </li>
                ))}
              </Stack>
            )}
          </Box>
        )}
        {(hasAppPlan || plan) && <TestResults app={app} />}
        <Flex gap={2} wrap="wrap">
          {services.length > 0 && (
            <Button
              size="sm"
              variant="ghost"
              onClick={() => setShowDetails(!showDetails)}
              data-testid="details-toggle"
            >
              {showDetails ? "Hide services" : "Services"}
            </Button>
          )}
          {!isGroup && (
            <Button
              size="sm"
              variant="ghost"
              onClick={() => setShowTags(!showTags)}
              data-testid="tags-toggle"
            >
              {showTags ? "Close tags" : "Tags"}
            </Button>
          )}
          {!hasAppPlan && items.length > 0 && (
            <Button
              size="sm"
              variant="outline"
              onClick={() => setShowCreate(!showCreate)}
              data-testid="create-plan-toggle"
            >
              {showCreate ? "Cancel" : "Create backup plan"}
            </Button>
          )}
          {hasDB && (
            <Button
              size="sm"
              variant="outline"
              loading={busy === "dump"}
              onClick={() =>
                act("dump", () => infraApi.dump(app.app), "Databases dumped")
              }
            >
              Test dump now
            </Button>
          )}
          {hasAppPlan && (
            <Button
              size="sm"
              variant="outline"
              loading={busy === "verify"}
              onClick={() =>
                act(
                  "verify",
                  () => infraApi.verify(app.app),
                  "Database restore test started",
                )
              }
              data-testid="verify-db"
            >
              Test database restore
            </Button>
          )}
          {(hasAppPlan || plan) && (
            <Button
              size="sm"
              variant="ghost"
              onClick={() => setShowRecovery(!showRecovery)}
              data-testid="recovery-toggle"
            >
              {showRecovery ? "Hide restore" : "Restore…"}
            </Button>
          )}
          {plan && (
            <Button
              size="sm"
              variant="outline"
              loading={busy === "restore"}
              onClick={() =>
                act(
                  "restore",
                  () => infraApi.restoreTest(plan),
                  "Restore test started",
                )
              }
            >
              Test file restore
            </Button>
          )}
        </Flex>
        {showTags && (
          <TagEditor
            app={app}
            onDone={() => {
              setShowTags(false);
              reload();
            }}
          />
        )}
        {showRecovery && <RecoveryPanel app={app.app} />}
        {showCreate && (
          <CreatePlanForm
            app={app}
            onDone={() => {
              setShowCreate(false);
              reload();
            }}
          />
        )}
      </Stack>
    </SectionCard>
  );
};

const ReadinessSection = ({ rd }: { rd: ReadinessReport | null }) => {
  if (!rd) return <Text fontSize="sm">Loading…</Text>;
  return (
    <Stack gap={4}>
      <SectionCard title="Overall">
        <Flex gap={2} align="center">
          <StateBadge state={rd.level} />
          <Text fontSize="sm" color="fg.muted">
            {fmt(rd.generated_at)}
          </Text>
        </Flex>
      </SectionCard>
      <SectionCard title="Repositories">
        <Stack gap={2} fontSize="sm">
          {(rd.repos || []).length === 0 && (
            <Text color="fg.muted">No repository configured yet.</Text>
          )}
          {(rd.repos || []).map((r) => (
            <Flex key={r.repo} gap={2} wrap="wrap" align="center">
              <Text fontWeight="medium">{r.repo}</Text>
              <Code fontSize="xs" wordBreak="break-all">
                {r.location}
              </Code>
              <Badge colorPalette={r.offsite ? "green" : "orange"}>
                {r.offsite ? "off-site" : "local"}
              </Badge>
              <Badge
                colorPalette={
                  !r.last_check ? "orange" : r.check_ok ? "green" : "red"
                }
              >
                {r.last_check
                  ? `check ${r.check_ok ? "ok" : "failed"} ${fmt(r.last_check)}`
                  : "never checked"}
              </Badge>
            </Flex>
          ))}
        </Stack>
      </SectionCard>
      {(rd.apps || []).map((a) => (
        <SectionCard key={a.app} title={a.name} description={a.app}>
          <Stack gap={1} fontSize="sm">
            <Box>
              <StateBadge state={a.level} />
            </Box>
            {a.checks.map((c) => (
              <Flex key={c.name} gap={2} align="flex-start">
                <Box flexShrink={0}>
                  <StateBadge state={c.status} />
                </Box>
                <Text wordBreak="break-word">
                  <Code fontSize="xs">{c.name}</Code> {c.detail}
                </Text>
              </Flex>
            ))}
          </Stack>
        </SectionCard>
      ))}
    </Stack>
  );
};

const ReportSection = () => {
  const [md, setMd] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const load = async () => {
    setBusy(true);
    try {
      setMd(await infraApi.report());
    } catch (e) {
      alerts.error(errMsg(e));
    } finally {
      setBusy(false);
    }
  };
  const download = () => {
    if (!md) return;
    const blob = new Blob([md], { type: "text/markdown" });
    const a = document.createElement("a");
    a.href = URL.createObjectURL(blob);
    a.download = `relyper-recovery-report-${new Date().toISOString().slice(0, 10)}.md`;
    a.click();
    URL.revokeObjectURL(a.href);
  };
  return (
    <SectionCard
      title="Disaster recovery report"
      description="Repositories, apps, protection state, test results and a restore runbook for the plain restic CLI. Contains no passwords – store it outside this server."
    >
      <Stack gap={3}>
        <Flex gap={2} wrap="wrap">
          <Button
            size="sm"
            loading={busy}
            onClick={load}
            data-testid="report-load"
          >
            {md ? "Refresh" : "Create report"}
          </Button>
          {md && (
            <Button size="sm" variant="outline" onClick={download}>
              Download .md
            </Button>
          )}
        </Flex>
        {md && (
          <Code
            as="pre"
            fontSize="xs"
            p={3}
            whiteSpace="pre-wrap"
            wordBreak="break-word"
            maxH="480px"
            overflowY="auto"
            data-testid="report-text"
          >
            {md}
          </Code>
        )}
      </Stack>
    </SectionCard>
  );
};

// Steps to connect Docker on Easypanel, shown while Docker is not reachable.
export const EasypanelDockerSteps = () => (
  <Box
    mt={3}
    fontSize="sm"
    wordBreak="break-word"
    css={{ "& code": { wordBreak: "break-all" } }}
    data-testid="easypanel-steps"
  >
    <Text fontWeight="medium">On Easypanel</Text>
    <Text mt={1}>
      Two App services in the same project, each with its own settings. Use one
      random token for both.
    </Text>
    <Text fontWeight="medium" mt={3}>
      1. New service <Code>docker-guard</Code>
    </Text>
    <Stack as="ul" gap={1} ps={5} mt={1} listStyleType="disc">
      <li>
        Build: this repository, Dockerfile <Code>Dockerfile.docker-guard</Code>.
        No domain, no port.
      </li>
      <li>
        Mount: <Code>/var/run/docker.sock</Code> →{" "}
        <Code>/var/run/docker.sock</Code> (only this service gets the socket).
      </li>
      <li>
        Environment:{" "}
        <Code>
          RELYPER_GUARD_TOKEN=&lt;token&gt; RELYPER_GUARD_DUMPS=1
          RELYPER_GUARD_VERIFY=1
        </Code>
      </li>
    </Stack>
    <Text fontWeight="medium" mt={3}>
      2. This service (Relyper Backup)
    </Text>
    <Stack as="ul" gap={1} ps={5} mt={1} listStyleType="disc">
      <li>
        Environment:{" "}
        <Code>RELYPER_GUARD_URL=tcp://&lt;project&gt;_docker-guard:2375</Code>{" "}
        and <Code>RELYPER_GUARD_TOKEN=&lt;token&gt;</Code> (same name and value
        as on <Code>docker-guard</Code>)
      </li>
      <li>
        Mounts (the app data to back up, read-only if possible):{" "}
        <Code>/var/lib/docker/volumes</Code> and{" "}
        <Code>/etc/easypanel/projects</Code>, each at the same path inside. No
        Docker socket here.
      </li>
    </Stack>
    <Text mt={3}>Deploy both, then press Refresh on this page.</Text>
  </Box>
);

export const InfraPage = () => {
  const [cov, setCov] = useState<CoverageReport | null>(null);
  const [services, setServices] = useState<Record<string, ServiceInfo[]>>({});
  // ?status=group:unprotected (from the dashboard) preselects the filter.
  const location = useLocation();
  const [filter, setFilter] = useState({
    text: "",
    kind: "",
    protection: new URLSearchParams(location.search).get("status") || "",
    db: "",
    tag: "",
  });
  const [rd, setRd] = useState<ReadinessReport | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [tab, setTab] = useState("apps");
  const [loading, setLoading] = useState(false);

  const reload = useCallback(async (refresh = false) => {
    setLoading(true);
    try {
      setCov(await infraApi.coverage(refresh));
      setError(null);
      infraApi
        .inventory()
        .then((inv) => {
          const m: Record<string, ServiceInfo[]> = {};
          for (const a of inv.apps || []) m[a.id] = a.services || [];
          setServices(m);
        })
        .catch(() => setServices({}));
    } catch (e) {
      setError(errMsg(e));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    reload();
  }, [reload]);
  useEffect(() => {
    if (tab === "readiness") {
      infraApi
        .readiness()
        .then(setRd)
        .catch((e) => setError(errMsg(e)));
    }
  }, [tab]);

  const docker = cov?.docker;
  // Tag groups are listed on their own and not counted with the apps, as
  // their members are already counted.
  const all = cov?.apps || [];
  const apps = all.filter((a) => a.kind !== "group");
  const groups = all.filter((a) => a.kind === "group");
  const appsById = Object.fromEntries(all.map((a) => [a.app, a]));
  const tags = Array.from(new Set(apps.flatMap((a) => a.tags || []))).sort();
  const counts = (s: string[]) =>
    apps.filter((a) => s.includes(a.protection)).length;
  const engines = Array.from(
    new Set(apps.flatMap((a) => a.databases || [])),
  ).sort();
  const shown = apps.filter(
    (a) =>
      (!filter.kind || a.kind === filter.kind) &&
      matchesStatus(filter.protection, a.protection) &&
      (!filter.db || (a.databases || []).includes(filter.db)) &&
      (!filter.tag || (a.tags || []).includes(filter.tag)) &&
      (!filter.text.trim() ||
        searchMatches(a, services[a.app] || [], filter.text).length > 0),
  );
  const shownGroups = groups.filter(
    (g) =>
      (!filter.tag || g.name === filter.tag) &&
      (!filter.text.trim() ||
        g.name.toLowerCase().includes(filter.text.trim().toLowerCase())),
  );
  const select = (
    key: "kind" | "db" | "tag",
    label: string,
    options: [string, string][],
  ) => (
    <NativeSelect.Root size="sm" width={{ base: "100%", md: "auto" }}>
      <NativeSelect.Field
        aria-label={label}
        data-testid={`filter-${key}`}
        value={filter[key]}
        onChange={(e) => setFilter({ ...filter, [key]: e.target.value })}
      >
        <option value="">{label}: all</option>
        {options.map(([v, l]) => (
          <option key={v} value={v}>
            {l}
          </option>
        ))}
      </NativeSelect.Field>
      <NativeSelect.Indicator />
    </NativeSelect.Root>
  );

  return (
    <Box maxW="1000px" data-testid="infra-page">
      <Flex justify="space-between" align="flex-start" gap={2} wrap="wrap">
        <Box>
          <Heading size="lg" mb={1}>
            Infrastructure
          </Heading>
          <Text color="fg.muted" mb={4} fontSize="sm">
            Apps on the Docker host and whether a completed backup protects
            them.
          </Text>
        </Box>
        <Button
          size="sm"
          variant="outline"
          mb={2}
          loading={loading}
          onClick={() => reload(true)}
        >
          Refresh
        </Button>
      </Flex>
      {error && (
        <Text color="red.500" mb={3}>
          {error}
        </Text>
      )}
      {docker && !docker.available && (
        <SectionCard title="Docker not reachable">
          {docker.hint && (
            <Text
              fontSize="sm"
              fontWeight="medium"
              color="red.600"
              _dark={{ color: "red.300" }}
              mb={2}
              data-testid="docker-hint"
            >
              {docker.hint}
            </Text>
          )}
          <Text fontSize="sm">
            Relyper Backup cannot reach Docker at <Code>{docker.endpoint}</Code>
            : {docker.error}. Run the Relyper Docker guard and set{" "}
            <Code>RELYPER_GUARD_URL</Code> and <Code>RELYPER_GUARD_TOKEN</Code>;
            see DEPLOYMENT.md. Backups of plain paths keep working without it.
          </Text>
          <EasypanelDockerSteps />
        </SectionCard>
      )}
      {docker?.available && (
        <Flex
          gap={2}
          mb={4}
          wrap="wrap"
          fontSize="sm"
          data-testid="infra-summary"
        >
          <Badge>Docker {docker.version}</Badge>
          {docker.swarm && <Badge>Swarm</Badge>}
          {docker.easypanel && <Badge>Easypanel</Badge>}
          {docker.access === "guard" ? (
            <Badge
              colorPalette="green"
              title={`Docker access through the Relyper Docker guard. Enabled: ${
                (docker.guard_features || []).join(", ") || "read only"
              }`}
            >
              Docker guard
            </Badge>
          ) : (
            docker.access === "unrestricted" && (
              <Badge
                colorPalette="orange"
                title="Relyper Backup has unrestricted Docker access (raw socket or a generic proxy). Whoever takes over Relyper Backup gets root on the host. Use the Relyper Docker guard, see DEPLOYMENT.md."
              >
                Unrestricted Docker access
              </Badge>
            )
          )}
          {protectionGroups.map((g) => {
            const n = counts(g.statuses);
            if (g.id === "nodata" && n === 0) return null;
            const value = "group:" + g.id;
            const active = filter.protection === value;
            return (
              <Badge
                key={g.id}
                as="button"
                colorPalette={g.color}
                variant={active ? "solid" : "subtle"}
                cursor="pointer"
                aria-pressed={active}
                title={
                  (active ? "Show all apps. " : "Show only these apps: ") +
                  g.statuses
                    .map((st) => stateLabel[st] || st.replace(/_/g, " "))
                    .join(", ")
                }
                data-testid={`summary-${g.id}`}
                onClick={() => {
                  setFilter({ ...filter, protection: active ? "" : value });
                  setTab("apps");
                }}
              >
                {n} {g.label}
              </Badge>
            );
          })}
        </Flex>
      )}
      <TabsRoot value={tab} onValueChange={(e: any) => setTab(e.value)}>
        <TabsList mb={4} flexWrap="wrap">
          <TabsTrigger value="apps">Apps</TabsTrigger>
          {docker?.available && (
            <TabsTrigger value="setup">Set up backups</TabsTrigger>
          )}
          <TabsTrigger value="readiness">Readiness</TabsTrigger>
          <TabsTrigger value="report">Recovery report</TabsTrigger>
        </TabsList>
        <TabsContent value="apps">
          <Stack gap={4}>
            {apps.length > 0 && (
              <Flex gap={2} wrap="wrap" data-testid="infra-filters">
                <Input
                  size="sm"
                  width={{ base: "100%", md: "200px" }}
                  placeholder="Search apps, services, images, domains, volumes"
                  aria-label="Search apps"
                  value={filter.text}
                  onChange={(e) =>
                    setFilter({ ...filter, text: e.target.value })
                  }
                />
                <NativeSelect.Root
                  size="sm"
                  width={{ base: "100%", md: "auto" }}
                >
                  <NativeSelect.Field
                    aria-label="Status"
                    data-testid="filter-protection"
                    value={filter.protection}
                    onChange={(e) =>
                      setFilter({ ...filter, protection: e.target.value })
                    }
                  >
                    <option value="">Status: all ({apps.length})</option>
                    {protectionGroups.map((g) => (
                      <optgroup
                        key={g.id}
                        label={`${g.label} (${counts(g.statuses)})`}
                      >
                        {g.statuses.length > 1 && (
                          <option value={"group:" + g.id}>
                            all {g.label} ({counts(g.statuses)})
                          </option>
                        )}
                        {g.statuses.map((st) => (
                          <option
                            key={st}
                            value={g.statuses.length > 1 ? st : "group:" + g.id}
                          >
                            {stateLabel[st] || st.replace(/_/g, " ")} (
                            {counts([st])})
                          </option>
                        ))}
                      </optgroup>
                    ))}
                  </NativeSelect.Field>
                  <NativeSelect.Indicator />
                </NativeSelect.Root>
                {select("kind", "Type", [
                  ["compose", "Compose"],
                  ["swarm_stack", "Swarm stack"],
                  ["easypanel", "Easypanel"],
                  ["container", "Container"],
                  ["system", "Hosting panel"],
                ])}
                {engines.length > 0 &&
                  select(
                    "db",
                    "Has database",
                    engines.map((e) => [e, e] as [string, string]),
                  )}
                {tags.length > 0 &&
                  select(
                    "tag",
                    "Tag",
                    tags.map((t) => [t, t] as [string, string]),
                  )}
              </Flex>
            )}
            {shownGroups.length > 0 && (
              <Stack gap={4} data-testid="tag-groups">
                <Heading size="sm">Tag groups</Heading>
                {shownGroups.map((g) => (
                  <AppCard
                    key={g.app}
                    app={g}
                    services={services[g.app] || []}
                    reload={() => reload()}
                    search={filter.text}
                    appsById={appsById}
                  />
                ))}
                <Heading size="sm">Apps</Heading>
              </Stack>
            )}
            {apps.length > 0 && shown.length === 0 && (
              <Text fontSize="sm" color="fg.muted">
                No app matches the filter.
              </Text>
            )}
            {docker?.available && apps.length === 0 && (
              <Text fontSize="sm" color="fg.muted">
                No containers found.
              </Text>
            )}
            {shown.map((a) => (
              <AppCard
                key={a.app}
                app={a}
                services={services[a.app] || []}
                reload={() => reload()}
                search={filter.text}
                appsById={appsById}
              />
            ))}
            {(cov?.plans_without_app || []).length > 0 && (
              <Text fontSize="xs" color="fg.muted">
                Plans not linked to an app: {cov?.plans_without_app?.join(", ")}
              </Text>
            )}
          </Stack>
        </TabsContent>
        <TabsContent value="setup">
          <SetupWizard
            apps={apps}
            easypanel={!!docker?.easypanel}
            onDone={() => reload(true)}
          />
        </TabsContent>
        <TabsContent value="readiness">
          <ReadinessSection rd={rd} />
        </TabsContent>
        <TabsContent value="report">
          <ReportSection />
        </TabsContent>
      </TabsRoot>
    </Box>
  );
};

export default InfraPage;
