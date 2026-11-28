// relyper: backup setup wizard on the Infrastructure page. Step 1 selects
// the whole Docker environment, all Easypanel projects or single apps; step
// 2 shows one proposed plan per app with the measured data size and lets the
// user set schedule, retention, exclusions, upload limit and compression;
// step 3 creates the plans. Single directories or databases outside an app
// use the normal "Add plan" form.
import React, { useEffect, useState } from "react";
import {
  Badge,
  Box,
  Flex,
  Input,
  NativeSelect,
  Stack,
  Text,
  Textarea,
} from "@chakra-ui/react";
import { Button } from "../../components/ui/button";
import { Field } from "../../components/ui/field";
import { Checkbox } from "../../components/ui/checkbox";
import { AddRepoLink } from "./AddRepoLink";
import { useConfig } from "../../app/provider";
import {
  AppCoverage,
  SetupOptions,
  SetupProposals,
  SetupResult,
  infraApi,
} from "./infraApi";

export const formatBytes = (n: number) => {
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i++;
  }
  return `${n.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
};

const errMsg = (e: any) => e?.message || String(e);

const Check = ({
  checked,
  onChange,
  children,
  testid,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  children: React.ReactNode;
  testid?: string;
}) => (
  <Checkbox
    checked={checked}
    onCheckedChange={(e) => onChange(!!e.checked)}
    size="sm"
    data-testid={testid}
  >
    {children}
  </Checkbox>
);

export const SetupWizard = ({
  apps,
  easypanel,
  onDone,
}: {
  apps: AppCoverage[];
  easypanel: boolean;
  onDone: () => void;
}) => {
  const [config] = useConfig();
  const repos = (config?.repos || []).filter((r) => !r.originInstanceId);
  const [scope, setScope] = useState("all");
  const [picked, setPicked] = useState<Record<string, boolean>>({});
  const [proposals, setProposals] = useState<SetupProposals | null>(null);
  const [include, setInclude] = useState<Record<string, boolean>>({});
  // Databases mode: shared plans (group name) per app; "" = own plan.
  const [groups, setGroups] = useState<string[]>([]);
  const [groupOf, setGroupOf] = useState<Record<string, string>>({});
  const [newGroup, setNewGroup] = useState("");
  const [opts, setOpts] = useState<SetupOptions | null>(null);
  const [excludes, setExcludes] = useState("");
  const [repo, setRepo] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [results, setResults] = useState<SetupResult[] | null>(null);

  useEffect(() => {
    if (!repo && repos.length > 0) setRepo(repos[0].id);
  }, [repos, repo]);

  const preview = async () => {
    setBusy(true);
    setError(null);
    setResults(null);
    try {
      const sel = Object.keys(picked).filter((k) => picked[k]);
      const p = await infraApi.proposeSetup(
        scope,
        scope === "apps" ? sel : undefined,
      );
      setProposals(p);
      setOpts(p.defaults);
      const inc: Record<string, boolean> = {};
      p.apps.forEach((a) => (inc[a.app] = !!a.plan && !a.existing_plan));
      setInclude(inc);
    } catch (e) {
      setError(errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  const create = async () => {
    if (!proposals || !opts) return;
    setBusy(true);
    setError(null);
    try {
      const chosen = proposals.apps
        .filter((a) => include[a.app])
        .map((a) => a.app);
      const shared: Record<string, string[]> = {};
      if (proposals.mode === "databases") {
        chosen
          .filter((id) => groupOf[id])
          .forEach((id) => (shared[groupOf[id]] ||= []).push(id));
      }
      const res = await infraApi.createSetup(
        chosen.filter((id) => !shared[groupOf[id]]),
        repo,
        {
          ...opts,
          excludes: excludes
            .split("\n")
            .map((s) => s.trim())
            .filter(Boolean),
        },
        proposals.mode,
        Object.keys(shared).length ? shared : undefined,
      );
      setResults(res);
      setProposals(null);
      onDone();
    } catch (e) {
      setError(errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  const num = (k: keyof SetupOptions, label: string) => (
    <Field label={label} w="90px">
      <Input
        size="sm"
        type="number"
        min={0}
        value={String(opts?.[k] ?? 0)}
        onChange={(e) =>
          setOpts({ ...opts!, [k]: parseInt(e.target.value || "0", 10) })
        }
      />
    </Field>
  );

  const chosen = proposals?.apps.filter((a) => include[a.app]) || [];
  const usedGroups = new Set(
    proposals?.mode === "databases"
      ? chosen.map((a) => groupOf[a.app]).filter(Boolean)
      : [],
  );
  const planCount =
    chosen.filter((a) => proposals?.mode !== "databases" || !groupOf[a.app])
      .length + usedGroups.size;
  const chosenBytes = chosen.reduce((n, a) => n + (a.size?.bytes || 0), 0);
  const chosenComplete = chosen.every((a) => a.size?.complete);
  const storage = proposals?.storage?.[repo];

  return (
    <Stack gap={4} fontSize="sm" data-testid="setup-wizard">
      <Text color="fg.muted">
        Creates one backup plan per app: its volumes, bind mounts and deployment
        files, plus consistent database dumps before every backup. For a single
        directory or database outside an app use “Add plan” in the sidebar.
      </Text>

      <Stack gap={2}>
        <Text fontWeight="medium">1. What should be backed up?</Text>
        <NativeSelect.Root size="sm" width={{ base: "100%", md: "380px" }}>
          <NativeSelect.Field
            value={scope}
            onChange={(e) => setScope(e.target.value)}
            data-testid="setup-scope"
          >
            <option value="all">Entire Docker environment</option>
            {easypanel && (
              <option value="easypanel_config">
                Easypanel configuration only (no app data)
              </option>
            )}
            {easypanel && (
              <option value="easypanel">Entire Easypanel environment</option>
            )}
            <option value="databases">
              Databases only (dumps of every database)
            </option>
            <option value="apps">Selected projects and apps</option>
          </NativeSelect.Field>
          <NativeSelect.Indicator />
        </NativeSelect.Root>
        {scope === "easypanel_config" && (
          <Text color="fg.muted" data-testid="setup-scope-help">
            One small plan for Easypanel itself: its settings (environment
            variables, domains, ports, Traefik routes, certificates) and what
            each service needs to deploy again (.env, compose files, Dockerfile;
            code only when it was uploaded, not from Git, an image or a
            template). No volumes, databases, dependencies or build output. Add
            the app plans later.
          </Text>
        )}
        {scope === "databases" && (
          <Text color="fg.muted" data-testid="setup-scope-help">
            One plan per app with a database server (PostgreSQL, MySQL, MariaDB,
            MongoDB, Redis), named &lt;plan&gt;-db, or shared plans for any
            selection of them: a consistent dump right before every backup,
            nothing else. App files and SQLite files need the app plans.
          </Text>
        )}
        {scope === "apps" && (
          <Flex gap={3} wrap="wrap">
            {apps.map((a) => (
              <Check
                key={a.app}
                checked={!!picked[a.app]}
                onChange={(v) => setPicked({ ...picked, [a.app]: v })}
              >
                {a.name}{" "}
                <Text as="span" color="fg.muted">
                  ({a.kind})
                </Text>
              </Check>
            ))}
          </Flex>
        )}
        <Box>
          <Button
            size="sm"
            onClick={preview}
            loading={busy && !proposals}
            disabled={scope === "apps" && !Object.values(picked).some((v) => v)}
            data-testid="setup-preview"
          >
            Propose plans
          </Button>
        </Box>
      </Stack>

      {error && <Text color="red.500">{error}</Text>}

      {proposals && opts && (
        <>
          <Stack gap={2} data-testid="setup-proposals">
            <Text fontWeight="medium">2. Proposed plans</Text>
            {proposals.apps.length === 0 && (
              <Text color="fg.muted">No apps found for this selection.</Text>
            )}
            {proposals.mode === "databases" && proposals.apps.length > 1 && (
              <Flex gap={2}>
                <Button
                  size="xs"
                  variant="outline"
                  onClick={() => {
                    const inc: Record<string, boolean> = {};
                    proposals.apps.forEach(
                      (a) => (inc[a.app] = !!a.plan && !a.existing_plan),
                    );
                    setInclude(inc);
                  }}
                >
                  Select all
                </Button>
                <Button
                  size="xs"
                  variant="outline"
                  onClick={() => setInclude({})}
                >
                  Select none
                </Button>
              </Flex>
            )}
            {proposals.mode === "databases" && (
              <Flex
                gap={2}
                wrap="wrap"
                align="center"
                data-testid="setup-groups"
              >
                <Text color="fg.muted">
                  Shared plans: one plan dumps all databases assigned to it (the
                  apps get its name as tag).
                </Text>
                <Input
                  size="xs"
                  width="160px"
                  placeholder="name, e.g. customers"
                  value={newGroup}
                  onChange={(e) => setNewGroup(e.target.value)}
                  data-testid="setup-new-group"
                />
                <Button
                  size="xs"
                  variant="outline"
                  disabled={
                    !/^[a-z0-9][a-z0-9_.-]{0,39}$/.test(
                      newGroup.trim().toLowerCase().replace(/\s+/g, "-"),
                    )
                  }
                  onClick={() => {
                    const g = newGroup
                      .trim()
                      .toLowerCase()
                      .replace(/\s+/g, "-");
                    if (!groups.includes(g)) setGroups([...groups, g]);
                    setNewGroup("");
                  }}
                  data-testid="setup-add-group"
                >
                  Add shared plan
                </Button>
                {groups.map((g) => (
                  <Badge key={g} colorPalette="purple">
                    {g}:{" "}
                    {
                      proposals.apps.filter(
                        (a) => include[a.app] && groupOf[a.app] === g,
                      ).length
                    }
                  </Badge>
                ))}
              </Flex>
            )}
            {(proposals.notes || []).map((n) => (
              <Text key={n} color="fg.muted">
                {n}
              </Text>
            ))}
            {proposals.apps.map((a) => (
              <Box
                key={a.app}
                borderWidth="1px"
                borderRadius="md"
                px={3}
                py={2}
                wordBreak="break-word"
              >
                <Flex gap={2} align="center" wrap="wrap">
                  <Check
                    checked={!!include[a.app]}
                    onChange={(v) => setInclude({ ...include, [a.app]: v })}
                    testid={`setup-include-${a.name}`}
                  >
                    <b>{a.name}</b>
                  </Check>
                  {proposals.mode === "databases" && groups.length > 0 ? (
                    <NativeSelect.Root size="xs" width="auto">
                      <NativeSelect.Field
                        value={groupOf[a.app] || ""}
                        onChange={(e) =>
                          setGroupOf({ ...groupOf, [a.app]: e.target.value })
                        }
                        data-testid={`setup-group-${a.name}`}
                      >
                        <option value="">
                          own plan {a.plan ? a.plan.plan_id : ""}
                        </option>
                        {groups.map((g) => (
                          <option key={g} value={g}>
                            shared plan app-group-{g}-db
                          </option>
                        ))}
                      </NativeSelect.Field>
                      <NativeSelect.Indicator />
                    </NativeSelect.Root>
                  ) : (
                    a.plan && <Badge>{a.plan.plan_id}</Badge>
                  )}
                  {a.has_databases && (
                    <Badge colorPalette="blue">databases</Badge>
                  )}
                  {a.existing_plan && (
                    <Badge colorPalette="orange">plan exists</Badge>
                  )}
                  {proposals.mode === "databases" ? (
                    <Text color="fg.muted">
                      size known after the first dump
                    </Text>
                  ) : a.size ? (
                    <Text color="fg.muted">
                      {a.size.complete ? "" : "at least "}
                      {formatBytes(a.size.bytes)} in {a.size.files} files
                    </Text>
                  ) : null}
                </Flex>
                {a.error && <Text color="red.500">{a.error}</Text>}
                {a.plan?.databases && a.plan.databases.length > 0 && (
                  <Text>Dumps: {a.plan.databases.join(", ")}</Text>
                )}
                {(a.plan?.skipped || []).map((s) => (
                  <Text key={s} color="orange.500">
                    Not included: {s}
                  </Text>
                ))}
                {(a.plan?.warnings || []).map((s) => (
                  <Text key={s} color="orange.500">
                    {s}
                  </Text>
                ))}
                {(a.plan?.notes || []).map((s) => (
                  <Text key={s} color="fg.muted">
                    {s}
                  </Text>
                ))}
                {a.plan?.sources && a.plan.sources.length > 0 && (
                  <details data-testid={`setup-sources-${a.name}`}>
                    <summary style={{ cursor: "pointer" }}>
                      Where the code comes from (
                      {a.plan.sources.filter((x) => x.code).length} of{" "}
                      {a.plan.sources.length} services uploaded, code backed up)
                    </summary>
                    <Stack gap={0.5} mt={1}>
                      {[...a.plan.sources]
                        .sort(
                          (x, y) =>
                            Number(y.code) - Number(x.code) ||
                            x.service.localeCompare(y.service),
                        )
                        .map((x) => (
                          <Flex key={x.service} gap={2} wrap="wrap">
                            <Badge
                              colorPalette={x.code ? "green" : "gray"}
                              flexShrink={0}
                            >
                              {x.source}
                            </Badge>
                            <Text fontWeight="medium">{x.service}</Text>
                            <Text color="fg.muted" minW={0}>
                              {x.code ? "code backed up: " : "no code: "}
                              {x.reason}
                            </Text>
                          </Flex>
                        ))}
                    </Stack>
                  </details>
                )}
                {a.items && a.items.length > 0 && (
                  <details>
                    <summary style={{ cursor: "pointer" }}>
                      What is in it ({a.items.length})
                    </summary>
                    <Stack
                      gap={0.5}
                      mt={1}
                      data-testid={`setup-items-${a.name}`}
                    >
                      {a.items.map((it) => (
                        <Flex key={it.path} gap={2} justify="space-between">
                          <Text minW={0} title={it.path}>
                            {it.label}
                          </Text>
                          <Text color="fg.muted" flexShrink={0}>
                            {it.complete ? "" : "≥ "}
                            {formatBytes(it.bytes)}
                          </Text>
                        </Flex>
                      ))}
                    </Stack>
                  </details>
                )}
              </Box>
            ))}
          </Stack>

          <Stack gap={3}>
            <Text fontWeight="medium">3. Options</Text>
            {repos.length === 0 ? (
              <AddRepoLink />
            ) : (
              <Field label="Repository" w={{ base: "100%", md: "260px" }}>
                <NativeSelect.Root size="sm">
                  <NativeSelect.Field
                    value={repo}
                    onChange={(e) => setRepo(e.target.value)}
                    data-testid="setup-repo"
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
            )}
            <Flex gap={3} wrap="wrap">
              <Field
                label="Apps with databases (cron)"
                w={{ base: "100%", md: "220px" }}
                helperText="Default: every 6 hours"
              >
                <Input
                  size="sm"
                  value={opts.database_cron}
                  onChange={(e) =>
                    setOpts({ ...opts, database_cron: e.target.value })
                  }
                />
              </Field>
              <Field
                label="Other apps (cron)"
                w={{ base: "100%", md: "220px" }}
                helperText="Default: daily at 02:30"
              >
                <Input
                  size="sm"
                  value={opts.files_cron}
                  onChange={(e) =>
                    setOpts({ ...opts, files_cron: e.target.value })
                  }
                />
              </Field>
            </Flex>
            <Flex gap={3} wrap="wrap">
              {num("daily", "Daily")}
              {num("weekly", "Weekly")}
              {num("monthly", "Monthly")}
              {num("yearly", "Yearly")}
            </Flex>
            <Flex gap={3} wrap="wrap" align="flex-start">
              <Field
                label="Exclude patterns"
                w={{ base: "100%", md: "260px" }}
                helperText="One glob per line, e.g. *.log"
              >
                <Textarea
                  size="sm"
                  rows={3}
                  value={excludes}
                  onChange={(e) => setExcludes(e.target.value)}
                />
              </Field>
              <Field
                label="Upload limit (KiB/s)"
                w="160px"
                helperText="0 = unlimited"
              >
                <Input
                  size="sm"
                  type="number"
                  min={0}
                  value={String(opts.limit_upload_kib || 0)}
                  onChange={(e) =>
                    setOpts({
                      ...opts,
                      limit_upload_kib: parseInt(e.target.value || "0", 10),
                    })
                  }
                />
              </Field>
              <Field label="Compression" w="140px">
                <NativeSelect.Root size="sm">
                  <NativeSelect.Field
                    value={opts.compression || "auto"}
                    onChange={(e) =>
                      setOpts({ ...opts, compression: e.target.value })
                    }
                  >
                    <option value="auto">auto</option>
                    <option value="max">max</option>
                    <option value="off">off</option>
                  </NativeSelect.Field>
                  <NativeSelect.Indicator />
                </NativeSelect.Root>
              </Field>
            </Flex>
            <Text color="fg.muted" data-testid="setup-estimate">
              {proposals.mode === "databases"
                ? "The size of the dumps is known after the first backup. "
                : null}
              Data to back up: {chosenComplete ? "" : "at least "}
              {formatBytes(chosenBytes)} (measured now, before deduplication and
              compression; database dumps not included).{" "}
              {storage
                ? `Free in ${repo}: ${formatBytes(storage.free_bytes)} of ${formatBytes(storage.total_bytes)}.`
                : `Free storage of ${repo} has not been measured.`}{" "}
              Repository checks and restore tests follow the repository and
              verification settings.
            </Text>
            <Box>
              <Button
                size="sm"
                colorPalette="blue"
                onClick={create}
                loading={busy}
                disabled={!repo || chosen.length === 0}
                data-testid="setup-create"
              >
                Create {planCount} plan{planCount === 1 ? "" : "s"}
              </Button>
            </Box>
          </Stack>
        </>
      )}

      {results && (
        <Stack gap={1} data-testid="setup-results">
          <Text fontWeight="medium">Result</Text>
          {results.map((r) => (
            <Text key={r.app} color={r.error ? "red.500" : "green.600"}>
              {r.app}: {r.error ? r.error : `plan ${r.plan_id} created`}
            </Text>
          ))}
          <Text color="fg.muted">
            The first backups run on schedule; start one now from the plan page
            if you like.
          </Text>
        </Stack>
      )}
    </Stack>
  );
};

export default SetupWizard;
