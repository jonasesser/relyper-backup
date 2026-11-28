// relyper: operator page "Customers" – tenants of the customer portal, their
// plans and repositories, e-mail recipients and key escrow
// (docs/CONCEPT-MULTITENANT.md, CUSTOMERS.md).
import React, { useCallback, useEffect, useState } from "react";
import {
  Badge,
  Box,
  Code,
  Flex,
  Input,
  NativeSelect,
  Stack,
  Text,
} from "@chakra-ui/react";
import { Button } from "../../components/ui/button";
import { Field } from "../../components/ui/field";
import { Checkbox } from "../../components/ui/checkbox";
import { SectionCard } from "../../components/common/SectionCard";
import { alerts } from "../../components/common/Alerts";
import { backendUrl } from "../../state/buildcfg";

const tokenKey = "backrest-ui-authToken";

export type Kit = {
  repo: string;
  keyId: string;
  createdAt: string;
  createdBy: string;
  escrowed: boolean;
  escrowFile?: string;
};

export type Tenant = {
  id: string;
  name: string;
  externalIds?: string[];
  suspended?: boolean;
  escrow?: boolean;
  plans?: string[];
  repos?: string[];
  ownRepos?: string[];
  notify: { recipients?: string[]; events?: string[] };
  kits?: Kit[];
};

export type ObjectRef = { id: string; repo?: string; tenant?: string };

export type TenantList = {
  tenants: Tenant[];
  plans: ObjectRef[];
  repos: ObjectRef[];
  events: string[];
  status?: Record<string, any>;
};

const url = (path: string) =>
  new URL(
    "relyper/tenants/" + path,
    new URL(backendUrl, window.location.href),
  ).toString();

async function call<T>(
  method: string,
  path: string,
  body?: unknown,
): Promise<T> {
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
  };
  const token = localStorage.getItem(tokenKey);
  if (token) headers["Authorization"] = "Bearer " + token;
  const resp = await window.fetch(url(path), {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await resp.text();
  let data: any = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    data = null;
  }
  if (!resp.ok)
    throw new Error(data?.error || `${resp.status} ${resp.statusText}`);
  return data as T;
}

export const tenantsApi = {
  list: () => call<TenantList>("GET", ""),
  create: (id: string, name: string, externalIds: string[]) =>
    call<Tenant>("POST", "", { id, name, externalIds }),
  update: (id: string, b: Partial<Tenant>) => call<Tenant>("PUT", id, b),
  remove: (id: string) => call<unknown>("DELETE", id),
  assignPlan: (id: string, plan: string) =>
    call<Tenant>("POST", `${id}/plans`, { plan }),
  unassignPlan: (id: string, plan: string) =>
    call<Tenant>("DELETE", `${id}/plans/${encodeURIComponent(plan)}`),
  assignRepo: (id: string, repo: string) =>
    call<Tenant>("POST", `${id}/repos`, { repo }),
  unassignRepo: (id: string, repo: string) =>
    call<Tenant>("DELETE", `${id}/repos/${encodeURIComponent(repo)}`),
};

const split = (s: string) =>
  s
    .split(/[\s,]+/)
    .map((x) => x.trim())
    .filter(Boolean);

const Setup = ({ status }: { status?: Record<string, any> }) => {
  if (!status) return null;
  const row = (ok: boolean, label: string, hint: string) => (
    <Flex gap={2} align="center">
      <Badge colorPalette={ok ? "green" : "gray"}>{ok ? "on" : "off"}</Badge>
      <Text fontSize="sm">
        {label}
        {!ok && (
          <Text as="span" color="fg.muted">
            {" "}
            – {hint}
          </Text>
        )}
      </Text>
    </Flex>
  );
  return (
    <Stack gap={1} data-testid="customers-status">
      {row(
        !!status.portal,
        "Customer portal",
        status.portalRequested
          ? "needs Relyper Identity sign-in (RELYPER_OIDC_*)"
          : "set RELYPER_PORTAL=1",
      )}
      {row(
        !!status.email,
        "E-mail notifications",
        status.emailError || "set RELYPER_SMTP_*",
      )}
      {row(
        !!status.escrow,
        "Key escrow",
        "set RELYPER_ESCROW_PUBLIC_KEY (relyper-escrow keygen)",
      )}
      <Text fontSize="xs" color="fg.muted">
        Customers sign in with the role{" "}
        <Code fontSize="xs">{status.customerRole}</Code> or{" "}
        <Code fontSize="xs">{status.customerAdmin}</Code> and the claim{" "}
        <Code fontSize="xs">{status.tenantClaim}</Code>. Portal link:{" "}
        <Code fontSize="xs">/#/portal</Code>
      </Text>
    </Stack>
  );
};

const TenantCard = ({
  t,
  list,
  reload,
}: {
  t: Tenant;
  list: TenantList;
  reload: () => void;
}) => {
  const [ext, setExt] = useState((t.externalIds || []).join(" "));
  const freePlans = list.plans.filter((p) => !p.tenant);
  const freeRepos = list.repos.filter((r) => !r.tenant);
  const [plan, setPlan] = useState("");
  const [repo, setRepo] = useState("");
  const run = async (f: () => Promise<unknown>, ok: string) => {
    try {
      await f();
      alerts.success(ok);
      reload();
    } catch (e: any) {
      alerts.error(e.message);
    }
  };
  const repoOf = (planID: string) =>
    list.plans.find((p) => p.id === planID)?.repo;
  return (
    <Box
      borderWidth="1px"
      borderRadius="md"
      p={3}
      data-testid={`tenant-${t.id}`}
    >
      <Flex gap={2} align="center" wrap="wrap" mb={2}>
        <Text fontWeight="semibold">{t.name}</Text>
        <Code fontSize="xs">{t.id}</Code>
        {t.suspended && <Badge colorPalette="red">suspended</Badge>}
        {t.escrow && <Badge colorPalette="purple">key escrow</Badge>}
      </Flex>
      <Stack gap={3}>
        <Flex gap={2} align="end" wrap="wrap">
          <Field label="Tenant claim values (empty = ID)" flex="1" minW="200px">
            <Input
              size="sm"
              value={ext}
              onChange={(e) => setExt(e.target.value)}
            />
          </Field>
          <Button
            size="sm"
            variant="outline"
            onClick={() =>
              run(
                () => tenantsApi.update(t.id, { externalIds: split(ext) }),
                "Saved",
              )
            }
          >
            Save
          </Button>
        </Flex>
        <Flex gap={4} wrap="wrap">
          <Checkbox
            checked={!!t.escrow}
            onCheckedChange={(e: any) =>
              run(
                () => tenantsApi.update(t.id, { escrow: !!e.checked }),
                "Saved",
              )
            }
          >
            <Text fontSize="sm">Key escrow booked</Text>
          </Checkbox>
          <Checkbox
            checked={!!t.suspended}
            onCheckedChange={(e: any) =>
              run(
                () => tenantsApi.update(t.id, { suspended: !!e.checked }),
                "Saved",
              )
            }
          >
            <Text fontSize="sm">
              Suspended (no sign-in; backups keep running)
            </Text>
          </Checkbox>
        </Flex>
        <Box>
          <Text fontSize="sm" fontWeight="semibold">
            Repositories
          </Text>
          <Flex gap={2} wrap="wrap" my={1}>
            {(t.repos || []).map((r) => (
              <Badge
                key={r}
                colorPalette={t.ownRepos?.includes(r) ? "purple" : "blue"}
              >
                {r}
                {t.ownRepos?.includes(r) ? " (customer)" : ""}
                <Box
                  as="span"
                  cursor="pointer"
                  ml={1}
                  onClick={() =>
                    run(() => tenantsApi.unassignRepo(t.id, r), "Removed")
                  }
                >
                  ×
                </Box>
              </Badge>
            ))}
          </Flex>
          <Flex gap={2}>
            <NativeSelect.Root size="sm" maxW="260px">
              <NativeSelect.Field
                value={repo}
                onChange={(e) => setRepo(e.target.value)}
                data-testid={`assign-repo-${t.id}`}
              >
                <option value="">Assign repository…</option>
                {freeRepos.map((r) => (
                  <option key={r.id} value={r.id}>
                    {r.id}
                  </option>
                ))}
              </NativeSelect.Field>
            </NativeSelect.Root>
            <Button
              size="sm"
              disabled={!repo}
              onClick={() =>
                run(() => tenantsApi.assignRepo(t.id, repo), "Assigned")
              }
            >
              Assign
            </Button>
          </Flex>
        </Box>
        <Box>
          <Text fontSize="sm" fontWeight="semibold">
            Plans
          </Text>
          <Flex gap={2} wrap="wrap" my={1}>
            {(t.plans || []).map((p) => {
              const hidden = !(t.repos || []).includes(repoOf(p) || "");
              return (
                <Badge
                  key={p}
                  colorPalette={hidden ? "orange" : "green"}
                  title={
                    hidden
                      ? "Not visible: its repository is not assigned to this customer"
                      : ""
                  }
                >
                  {p}
                  {hidden ? " (repository not assigned)" : ""}
                  <Box
                    as="span"
                    cursor="pointer"
                    ml={1}
                    onClick={() =>
                      run(() => tenantsApi.unassignPlan(t.id, p), "Removed")
                    }
                  >
                    ×
                  </Box>
                </Badge>
              );
            })}
          </Flex>
          <Flex gap={2}>
            <NativeSelect.Root size="sm" maxW="260px">
              <NativeSelect.Field
                value={plan}
                onChange={(e) => setPlan(e.target.value)}
                data-testid={`assign-plan-${t.id}`}
              >
                <option value="">Assign plan…</option>
                {freePlans.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.id} → {p.repo}
                  </option>
                ))}
              </NativeSelect.Field>
            </NativeSelect.Root>
            <Button
              size="sm"
              disabled={!plan}
              onClick={() =>
                run(() => tenantsApi.assignPlan(t.id, plan), "Assigned")
              }
            >
              Assign
            </Button>
          </Flex>
        </Box>
        <Text fontSize="sm">
          E-mail: {(t.notify.recipients || []).join(", ") || "no recipients"} ·
          events: {(t.notify.events || []).join(", ") || "none"}
        </Text>
        {(t.kits || []).length > 0 && (
          <Text fontSize="sm">
            Recovery kits:{" "}
            {(t.kits || [])
              .map(
                (k) =>
                  `${k.repo} (key ${k.keyId}, ${new Date(k.createdAt).toLocaleDateString()}${k.escrowed ? `, sealed: ${k.escrowFile}` : ""})`,
              )
              .join("; ")}
          </Text>
        )}
        <Box>
          <Button
            size="xs"
            variant="ghost"
            colorPalette="red"
            onClick={() => {
              if (
                window.confirm(
                  `Delete customer ${t.name}? Plans and repositories stay; they fall back to you.`,
                )
              )
                run(() => tenantsApi.remove(t.id), "Deleted");
            }}
          >
            Delete customer
          </Button>
        </Box>
      </Stack>
    </Box>
  );
};

export const CustomersPage = () => {
  const [list, setList] = useState<TenantList | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [id, setId] = useState("");
  const [name, setName] = useState("");
  const [ext, setExt] = useState("");
  const load = useCallback(() => {
    tenantsApi
      .list()
      .then((l) => {
        setList(l);
        setError(null);
      })
      .catch((e) => setError(e.message));
  }, []);
  useEffect(load, [load]);
  if (error) return <Text color="red.500">{error}</Text>;
  if (!list) return <Text>Loading…</Text>;
  const create = async () => {
    try {
      await tenantsApi.create(id.trim(), name.trim(), split(ext));
      alerts.success("Customer created");
      setId("");
      setName("");
      setExt("");
      load();
    } catch (e: any) {
      alerts.error(e.message);
    }
  };
  return (
    <Box>
      <SectionCard
        title="Customer portal"
        description="Customers sign in and see only the plans and repositories you assign to them."
      >
        <Setup status={list.status} />
      </SectionCard>
      <SectionCard title="New customer">
        <Flex gap={2} align="end" wrap="wrap">
          <Field label="ID" w="180px">
            <Input
              size="sm"
              value={id}
              placeholder="acme"
              onChange={(e) => setId(e.target.value)}
              data-testid="tenant-id"
            />
          </Field>
          <Field label="Name" flex="1" minW="180px">
            <Input
              size="sm"
              value={name}
              placeholder="ACME GmbH"
              onChange={(e) => setName(e.target.value)}
              data-testid="tenant-name"
            />
          </Field>
          <Field label="Tenant claim values (optional)" flex="1" minW="180px">
            <Input
              size="sm"
              value={ext}
              onChange={(e) => setExt(e.target.value)}
            />
          </Field>
          <Button
            size="sm"
            onClick={create}
            disabled={!id || !name}
            data-testid="tenant-create"
          >
            Create
          </Button>
        </Flex>
      </SectionCard>
      <SectionCard title={`Customers (${list.tenants.length})`}>
        <Stack gap={3}>
          {list.tenants.length === 0 && (
            <Text color="fg.muted">No customers yet.</Text>
          )}
          {list.tenants.map((t) => (
            <TenantCard key={t.id} t={t} list={list} reload={load} />
          ))}
        </Stack>
      </SectionCard>
    </Box>
  );
};

export default CustomersPage;
