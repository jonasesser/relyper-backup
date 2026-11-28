// relyper: storage use per repository and plan (Infrastructure page). The
// numbers are restic's own: repository statistics of the latest stats run
// and the size summary of every kept snapshot.
import React, { useCallback, useEffect, useState } from "react";
import { create } from "@bufbuild/protobuf";
import { Badge, Box, Flex, Stack, Table, Text } from "@chakra-ui/react";
import { Button } from "../../components/ui/button";
import { SectionCard } from "../../components/common/SectionCard";
import { alerts } from "../../components/common/Alerts";
import { backrestService } from "../../api/client";
import {
  DoRepoTaskRequest_Task,
  DoRepoTaskRequestSchema,
} from "../../../gen/ts/v1/service_pb";
import { infraApi, PlanUsage, RepoUsage } from "./infraApi";
import { PinButton } from "./PinButton";

export const bytes = (n?: number) => {
  if (!n) return "0 B";
  const u = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < u.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v >= 100 || i === 0 ? Math.round(v) : v.toFixed(1)} ${u[i]}`;
};

const when = (s?: string) => (s ? new Date(s).toLocaleString() : "never");

const PlanRow = ({ p, total }: { p: PlanUsage; total: number }) => {
  const share = total > 0 ? Math.min(100, (p.added_bytes / total) * 100) : 0;
  return (
    <Table.Row data-testid={`usage-plan-${p.plan}`}>
      <Table.Cell>
        <Text fontWeight="medium" wordBreak="break-word">
          {p.plan}
        </Text>
        {(p.apps || []).length > 0 && (
          <Text fontSize="xs" color="fg.muted">
            {p.apps!.join(", ")}
          </Text>
        )}
      </Table.Cell>
      <Table.Cell textAlign="end">{p.snapshots}</Table.Cell>
      <Table.Cell textAlign="end">
        {bytes(p.latest_size_bytes)}
        <Text fontSize="xs" color="fg.muted">
          +{bytes(p.latest_added_bytes)} new
        </Text>
      </Table.Cell>
      <Table.Cell textAlign="end">
        {bytes(p.added_bytes)}
        {total > 0 && (
          <Box mt={1} h="4px" bg="bg.muted" rounded="full" minW="60px">
            <Box h="4px" w={`${share}%`} bg="blue.500" rounded="full" />
          </Box>
        )}
        {p.without_summary ? (
          <Text fontSize="xs" color="fg.muted">
            {p.without_summary} without size
          </Text>
        ) : null}
      </Table.Cell>
    </Table.Row>
  );
};

const RepoCard = ({ r, reload }: { r: RepoUsage; reload: () => void }) => {
  const [busy, setBusy] = useState(false);
  const added = r.plans.reduce((n, p) => n + p.added_bytes, 0);
  const measure = async () => {
    setBusy(true);
    try {
      await backrestService.doRepoTask(
        create(DoRepoTaskRequestSchema, {
          repoId: r.repo,
          task: DoRepoTaskRequest_Task.STATS,
        }),
      );
      alerts.success("Measuring started");
      reload();
      // restic stats runs as an operation; pick up its result.
      setTimeout(reload, 4000);
    } catch (e: any) {
      alerts.error(e?.message || String(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <SectionCard title={r.repo} description={r.location}>
      <Stack gap={3} data-testid={`usage-repo-${r.repo}`}>
        <Flex gap={2} wrap="wrap" align="center" fontSize="sm">
          <Badge colorPalette="blue" size="lg">
            {r.stored_bytes ? bytes(r.stored_bytes) : "not measured"} on the
            storage
          </Badge>
          {r.uncompressed_bytes ? (
            <Text color="fg.muted">
              {bytes(r.uncompressed_bytes)} before compression · {r.snapshots}{" "}
              snapshots
            </Text>
          ) : null}
          {r.storage && (
            <Text color={r.storage.low ? "red.500" : "fg.muted"}>
              {bytes(r.storage.free_bytes)} free of{" "}
              {bytes(r.storage.total_bytes)}
            </Text>
          )}
          <Button size="xs" variant="outline" loading={busy} onClick={measure}>
            Measure now
          </Button>
        </Flex>
        <Text fontSize="xs" color="fg.muted">
          Measured {when(r.stats_at)} (restic stats, after deduplication and
          compression).
        </Text>
        {r.plans.length === 0 ? (
          <Text fontSize="sm" color="fg.muted">
            No plan stores its backups here.
          </Text>
        ) : (
          <Box overflowX="auto">
            <Table.Root size="sm" minW="520px">
              <Table.Header>
                <Table.Row>
                  <Table.ColumnHeader>Plan · apps</Table.ColumnHeader>
                  <Table.ColumnHeader textAlign="end">
                    Snapshots
                  </Table.ColumnHeader>
                  <Table.ColumnHeader textAlign="end">
                    Latest backup
                  </Table.ColumnHeader>
                  <Table.ColumnHeader textAlign="end">
                    Stored by plan
                  </Table.ColumnHeader>
                </Table.Row>
              </Table.Header>
              <Table.Body>
                {r.plans.map((p) => (
                  <PlanRow key={p.plan} p={p} total={added} />
                ))}
              </Table.Body>
            </Table.Root>
          </Box>
        )}
        <Box data-testid={`usage-pinned-${r.repo}`}>
          <Text fontSize="sm" fontWeight="medium">
            Pinned snapshots ({(r.pinned || []).length})
          </Text>
          {(r.pinned || []).length === 0 ? (
            <Text fontSize="xs" color="fg.muted">
              None. Pin a snapshot on its plan page (“Pin (keep forever)”) to
              keep it no matter what the retention says, e.g. a known good state
              before a big change.
            </Text>
          ) : (
            <Stack gap={1} mt={1}>
              {r.pinned!.map((s) => (
                <Flex
                  key={s.id}
                  gap={2}
                  wrap="wrap"
                  align="center"
                  justify="space-between"
                  fontSize="sm"
                >
                  <Text>
                    {when(s.time)} · {s.plan || "no plan"} ·{" "}
                    <Text as="span" fontFamily="mono">
                      {s.id.slice(0, 8)}
                    </Text>
                    {s.size_bytes ? ` · ${bytes(s.size_bytes)}` : ""}
                  </Text>
                  <PinButton
                    repo={r.repo}
                    snapshot={s.id}
                    tags={["relyper-pinned"]}
                    onDone={() => setTimeout(reload, 3000)}
                  />
                </Flex>
              ))}
            </Stack>
          )}
        </Box>
        <Text fontSize="xs" color="fg.muted">
          Latest backup: size of the files in the newest snapshot (what a
          restore writes) and what it added. Stored by plan: what the kept
          snapshots of the plan added to the repository (before compression),
          about its share; data several plans share counts for the plan that
          added it first.
        </Text>
      </Stack>
    </SectionCard>
  );
};

export const StorageUsage = () => {
  const [repos, setRepos] = useState<RepoUsage[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const load = useCallback(() => {
    infraApi
      .usage()
      .then((r) => {
        setRepos(r);
        setError(null);
      })
      .catch((e) => setError(e instanceof Error ? e.message : String(e)));
  }, []);
  useEffect(() => {
    load();
  }, [load]);
  if (error) return <Text color="red.500">{error}</Text>;
  if (!repos) return <Text color="fg.muted">Loading…</Text>;
  if (repos.length === 0)
    return <Text color="fg.muted">No repository is configured.</Text>;
  return (
    <Stack gap={4}>
      {repos.map((r) => (
        <RepoCard key={r.repo} r={r} reload={load} />
      ))}
    </Stack>
  );
};
