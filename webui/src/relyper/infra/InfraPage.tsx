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
  infraApi,
} from "./infraApi";

export const stateColor: Record<string, string> = {
  protected: "green",
  covered_by_dump: "green",
  ready: "green",
  ok: "green",
  remote: "gray",
  no_data: "gray",
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
};

// Anonymous volumes carry a 64-hex name; bind mounts repeat their path.
export const itemName = (it: DataItem) => {
  if (it.name === it.path) return "";
  if (/^[0-9a-f]{64}$/.test(it.name))
    return `anonymous volume ${it.name.slice(0, 12)}`;
  return it.name;
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

  useEffect(() => {
    infraApi
      .propose(app.app)
      .then((p) => {
        setProposal(p);
        setPlanId(p.plan_id);
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
      await infraApi.createPlan(app.app, repo, planId);
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
      <Stack gap={1}>
        {proposal.paths.map((p) => (
          <Code key={p} fontSize="xs" wordBreak="break-all">
            {p}
          </Code>
        ))}
      </Stack>
      {proposal.databases && proposal.databases.length > 0 && (
        <Text>
          Databases dumped right before every backup:{" "}
          {proposal.databases.join(", ")}. A failed dump fails the backup.
        </Text>
      )}
      {(proposal.skipped || []).map((s) => (
        <Text key={s} color="orange.500">
          Not included: {s}
        </Text>
      ))}
      {(proposal.warnings || []).map((s) => (
        <Text key={s} color="orange.500">
          {s}
        </Text>
      ))}
      {repos.length === 0 ? (
        <Text color="red.500">Add a repository first.</Text>
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

const AppCard = ({ app, reload }: { app: AppCoverage; reload: () => void }) => {
  const [showCreate, setShowCreate] = useState(false);
  const [busy, setBusy] = useState<string | null>(null);
  const hasDB = app.items.some((i) => i.kind === "database");
  const hasAppPlan = app.items.some((i) => i.kind === "database" && i.plan);
  const plan = (app.plans || [])[0];

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
    <SectionCard
      title={app.name}
      description={`${app.app} · ${app.kind.replace("_", " ")}`}
    >
      <Stack gap={3} data-testid={`app-${app.app}`}>
        <Flex gap={2} align="center" wrap="wrap">
          <StateBadge state={app.state} />
          {(app.plans || []).map((p) => (
            <Badge key={p} variant="outline">
              plan {p}
            </Badge>
          ))}
        </Flex>
        <Stack gap={0} fontSize="sm">
          {app.items.map((it, i) => (
            <Flex
              key={i}
              gap={2}
              py={1.5}
              borderTopWidth={i ? "1px" : 0}
              wrap="wrap"
              align="center"
            >
              <Box minW="0" flex="1 1 240px">
                <Text fontWeight="medium" wordBreak="break-word">
                  {it.service} · {it.kind.replace("_", " ")}{" "}
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
          {app.items.length === 0 && (
            <Text color="fg.muted">Nothing to back up.</Text>
          )}
        </Stack>
        {(app.notes || []).map((n) => (
          <Text key={n} fontSize="xs" color="fg.muted">
            {n}
          </Text>
        ))}
        {(hasAppPlan || plan) && <TestResults app={app} />}
        <Flex gap={2} wrap="wrap">
          {!hasAppPlan && app.items.length > 0 && (
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

export const InfraPage = () => {
  const [cov, setCov] = useState<CoverageReport | null>(null);
  const [rd, setRd] = useState<ReadinessReport | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [tab, setTab] = useState("apps");
  const [loading, setLoading] = useState(false);

  const reload = useCallback(async (refresh = false) => {
    setLoading(true);
    try {
      setCov(await infraApi.coverage(refresh));
      setError(null);
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
  const apps = cov?.apps || [];
  const counts = (s: string[]) =>
    apps.filter((a) => s.includes(a.state)).length;

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
          <Text fontSize="sm">
            Relyper Backup cannot reach Docker at <Code>{docker.endpoint}</Code>
            : {docker.error}. Mount the Docker socket (ideally through a socket
            proxy) and set <Code>RELYPER_DOCKER_HOST</Code>; see DEPLOYMENT.md.
            Backups of plain paths keep working without it.
          </Text>
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
          <Badge colorPalette="green">{counts(["protected"])} protected</Badge>
          <Badge colorPalette="orange">
            {counts(["stale", "not_in_snapshot", "disabled", "remote"])} need
            attention
          </Badge>
          <Badge colorPalette="red">
            {counts(["failing", "unprotected", "not_reachable"])} unprotected
          </Badge>
        </Flex>
      )}
      <TabsRoot value={tab} onValueChange={(e: any) => setTab(e.value)}>
        <TabsList mb={4} flexWrap="wrap">
          <TabsTrigger value="apps">Apps</TabsTrigger>
          <TabsTrigger value="readiness">Readiness</TabsTrigger>
          <TabsTrigger value="report">Recovery report</TabsTrigger>
        </TabsList>
        <TabsContent value="apps">
          <Stack gap={4}>
            {docker?.available && apps.length === 0 && (
              <Text fontSize="sm" color="fg.muted">
                No containers found.
              </Text>
            )}
            {apps.map((a) => (
              <AppCard key={a.app} app={a} reload={() => reload()} />
            ))}
            {(cov?.plans_without_app || []).length > 0 && (
              <Text fontSize="xs" color="fg.muted">
                Plans not linked to an app: {cov?.plans_without_app?.join(", ")}
              </Text>
            )}
          </Stack>
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
