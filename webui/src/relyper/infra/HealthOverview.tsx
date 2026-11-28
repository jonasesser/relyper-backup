// relyper: backup health summary at the top of the dashboard – app
// protection, recent failures, running operations, the latest verified
// restores, repository checks and measured free storage.
import React, { useEffect, useState } from "react";
import { Badge, Box, Flex, Heading, Stack, Text } from "@chakra-ui/react";
import { Link } from "react-router";
import { Overview, infraApi } from "./infraApi";
import { StateBadge } from "./InfraPage";

const when = (s?: string) => (s ? new Date(s).toLocaleString() : "never");

const gb = (n: number) => `${(n / 1024 ** 3).toFixed(1)} GB`;

const Tile = ({
  label,
  value,
  tone,
  testid,
  to,
}: {
  label: string;
  value: React.ReactNode;
  tone?: string;
  testid?: string;
  to?: string;
}) => {
  const tile = (
    <Box
      borderWidth="1px"
      borderRadius="md"
      px={3}
      py={2}
      h="100%"
      _hover={to ? { bg: "bg.muted" } : undefined}
      data-testid={testid}
    >
      <Text fontSize="xs" color="fg.muted">
        {label}
      </Text>
      <Text fontSize="xl" fontWeight="semibold" color={tone}>
        {value}
      </Text>
    </Box>
  );
  return (
    <Box minW="140px" flex="1 1 140px">
      {to ? <Link to={to}>{tile}</Link> : tile}
    </Box>
  );
};

export const HealthOverview = () => {
  const [o, setO] = useState<Overview | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    const load = () =>
      infraApi
        .overview()
        .then((r) => alive && (setO(r), setError(null)))
        .catch((e) => alive && setError(e?.message || String(e)));
    load();
    const t = setInterval(load, 30000);
    return () => {
      alive = false;
      clearInterval(t);
    };
  }, []);

  if (error) {
    return (
      <Text fontSize="sm" color="fg.muted" mb={4}>
        Backup health is not available: {error}
      </Text>
    );
  }
  if (!o) return null;
  const count = (keys: string[]) =>
    keys.reduce((n, k) => n + (o.apps[k] || 0), 0);
  const attention = count([
    "restore_not_verified",
    "partially_protected",
    "backup_overdue",
    "unsupported_configuration",
  ]);
  const bad = count(["not_protected", "backup_failed"]);

  return (
    <Stack gap={3} mb={6} data-testid="health-overview">
      <Flex align="center" gap={2} wrap="wrap">
        <Heading size="md">Backup health</Heading>
        <StateBadge state={o.level} />
        <Text fontSize="xs" color="fg.muted">
          measured {when(o.generated_at)}
        </Text>
      </Flex>
      {(o.attention || []).length > 0 && (
        <Box
          borderWidth="1px"
          borderColor="orange.300"
          borderRadius="md"
          px={3}
          py={2}
          fontSize="sm"
          data-testid="health-attention"
        >
          <Text fontWeight="medium">Needs attention</Text>
          {o.attention!.map((a) => {
            // App lines lead to the Infrastructure page, filtered.
            const to = /^\d+ apps? /.test(a)
              ? "/infra?status=group:unprotected"
              : undefined;
            return (
              <Text key={a}>
                •{" "}
                {to ? (
                  <Link to={to} style={{ textDecoration: "underline" }}>
                    {a}
                  </Link>
                ) : (
                  a
                )}
              </Text>
            );
          })}
        </Box>
      )}
      <Flex gap={2} wrap="wrap">
        {o.apps_total > 0 && (
          <>
            <Tile
              testid="tile-protected"
              label="Apps fully protected"
              value={`${o.apps["fully_protected"] || 0} / ${o.apps_total}`}
              tone="green.600"
              to="/infra?status=group:protected"
            />
            <Tile
              label="Apps needing attention"
              value={attention}
              tone={attention ? "orange.500" : undefined}
              to="/infra?status=group:attention"
            />
            <Tile
              testid="tile-unprotected"
              label="Apps unprotected or failed"
              value={bad}
              tone={bad ? "red.500" : undefined}
              to="/infra?status=group:unprotected"
            />
          </>
        )}
        <Tile
          label="Backups in 24 h"
          value={
            <>
              {o.backups_24h["success"] || 0}
              {(o.backups_24h["warning"] || 0) > 0 && (
                <Text as="span" color="orange.500">
                  {" "}
                  / {o.backups_24h["warning"]} warn
                </Text>
              )}
              {(o.backups_24h["error"] || 0) > 0 && (
                <Text as="span" color="red.500">
                  {" "}
                  / {o.backups_24h["error"]} failed
                </Text>
              )}
            </>
          }
        />
        <Tile
          testid="tile-failed"
          label="Failed operations in 24 h"
          value={o.failed_count_24h}
          tone={o.failed_count_24h ? "red.500" : undefined}
        />
        <Tile label="Running now" value={o.running.length} />
      </Flex>
      <Stack gap={1} fontSize="sm">
        <Text>
          Last successful backup: <b>{when(o.last_successful_backup?.time)}</b>
          {o.last_successful_backup?.plan &&
            ` (plan ${o.last_successful_backup.plan})`}
        </Text>
        <Text>
          Last repository check without errors:{" "}
          <b>{when(o.last_successful_check?.time)}</b>
          {o.last_successful_check && ` (${o.last_successful_check.repo})`}
        </Text>
        <Text>
          Last verified database restore:{" "}
          <b>{when(o.last_verified_database_restore?.time)}</b>
          {o.last_verified_database_restore &&
            ` (${o.last_verified_database_restore.subject})`}
        </Text>
        <Text>
          Last verified file restore:{" "}
          <b>{when(o.last_verified_file_restore?.time)}</b>
          {o.last_verified_file_restore &&
            ` (plan ${o.last_verified_file_restore.subject})`}
        </Text>
        {o.failed_24h.map((f) => (
          <Text key={f.id} color="red.500" wordBreak="break-word">
            {f.kind} {f.plan && f.plan !== "_system_" ? `of ${f.plan} ` : ""}(
            {f.repo}) failed {when(f.time)}: {f.message}
          </Text>
        ))}
      </Stack>
      {o.repos.some((r) => r.storage) && (
        <Stack gap={1} fontSize="sm">
          {o.repos
            .filter((r) => r.storage)
            .map((r) => {
              const st = r.storage!;
              const used = 1 - st.free_bytes / st.total_bytes;
              return (
                <Box key={r.repo}>
                  <Flex justify="space-between" gap={2} wrap="wrap">
                    <Text>
                      Storage {r.repo}{" "}
                      {st.low && <Badge colorPalette="red">almost full</Badge>}
                    </Text>
                    <Text color="fg.muted" fontSize="xs">
                      {gb(st.free_bytes)} free of {gb(st.total_bytes)} ·
                      measured {when(st.checked_at)}
                    </Text>
                  </Flex>
                  <Box h="6px" bg="bg.muted" borderRadius="full">
                    <Box
                      h="6px"
                      w={`${Math.round(used * 100)}%`}
                      bg={st.low ? "red.500" : "green.500"}
                      borderRadius="full"
                    />
                  </Box>
                </Box>
              );
            })}
        </Stack>
      )}
      {o.docker_available && (
        <Text fontSize="sm">
          <Link to="/infra">Open Infrastructure for details per app →</Link>
          {" · "}
          <Link to="/infra?tab=report">Disaster recovery guide →</Link>
        </Text>
      )}
    </Stack>
  );
};

export default HealthOverview;
