// relyper: customer portal (docs/CONCEPT-MULTITENANT.md). A separate screen
// at #/portal that never loads the operator UI: customers see their plans,
// runs, snapshots and storage, set e-mail notifications, connect their own
// storage and create recovery kits.
import React, { useCallback, useEffect, useState } from "react";
import {
  Badge,
  Box,
  Code,
  Container,
  Flex,
  Heading,
  Input,
  NativeSelect,
  Stack,
  Table,
  Text,
} from "@chakra-ui/react";
import { Button } from "../../components/ui/button";
import { Field } from "../../components/ui/field";
import { Checkbox } from "../../components/ui/checkbox";
import { SectionCard } from "../../components/common/SectionCard";
import { alerts } from "../../components/common/Alerts";
import IconSvg from "../../../assets/relyper-icon.svg";
import { UserMenuView } from "../identity/UserMenu";
import {
  Me,
  Notify,
  Overview,
  PlanView,
  PortalError,
  RepoView,
  RunView,
  SnapshotView,
  StorageRequest,
  authUrl,
  completePortalHandoff,
  formatBytes,
  formatTime,
  getPortalToken,
  portalApi,
  setPortalToken,
} from "./portalApi";

export const stateLabel: Record<string, [string, string]> = {
  ok: ["OK", "green"],
  warning: ["Warnings", "orange"],
  failing: ["Failed", "red"],
  stale: ["No recent backup", "orange"],
  never_run: ["Not run yet", "gray"],
  disabled: ["Paused", "gray"],
  running: ["Running", "blue"],
};

const runColor: Record<string, string> = {
  success: "green",
  error: "red",
  warning: "orange",
  inprogress: "blue",
};

const runLabel: Record<string, string> = {
  pending: "scheduled",
  inprogress: "running",
};

const eventLabel: Record<string, string> = {
  failure: "A backup failed",
  warning: "A backup finished with warnings",
  success: "A backup succeeded",
  stale: "No successful backup for a long time",
};

const loginErrors: Record<string, string> = {
  forbidden:
    "Your account has no access to the customer portal. Ask your provider to enable it.",
  missing_email: "Your account has no e-mail address.",
  invalid_state:
    "The sign-in expired or was started in another browser. Try again.",
  provider_unavailable:
    "The sign-in service cannot be reached. Try again later.",
  provider_error: "The sign-in was cancelled.",
};

const clearLoginQuery = () => {
  const url = new URL(window.location.href);
  url.searchParams.delete("relyper_login");
  url.searchParams.delete("relyper_login_error");
  window.history.replaceState(null, "", url.toString());
};

const StateBadge = ({ state }: { state: string }) => {
  const [label, color] = stateLabel[state] || [state, "gray"];
  return (
    <Badge colorPalette={color} data-testid="plan-state">
      {label}
    </Badge>
  );
};

// ---- sign-in ----

const SignIn = ({ error }: { error: string | null }) => {
  const [busy, setBusy] = useState(false);
  return (
    <Container maxW="md" py={16}>
      <Stack gap={6} align="stretch">
        <Flex align="center" gap={3} justify="center">
          <img src={IconSvg} alt="" style={{ height: 36 }} />
          <Heading size="lg">Relyper Backup</Heading>
        </Flex>
        <Text textAlign="center" color="fg.muted">
          Customer portal: see the state of your backups, get notified and keep
          your recovery kit.
        </Text>
        {error && (
          <Text color="red.500" data-testid="portal-login-error">
            {error}
          </Text>
        )}
        <Button
          colorPalette="blue"
          loading={busy}
          data-testid="portal-login"
          onClick={() => {
            setBusy(true);
            window.location.assign(authUrl("auth/oidc/login?portal=1"));
          }}
        >
          Sign in with Relyper
        </Button>
      </Stack>
    </Container>
  );
};

// ---- plans ----

const PlanDetails = ({ plan }: { plan: PlanView }) => {
  const [snaps, setSnaps] = useState<SnapshotView[] | null>(null);
  const [runs, setRuns] = useState<RunView[] | null>(null);
  useEffect(() => {
    portalApi
      .snapshots(plan.id)
      .then(setSnaps)
      .catch(() => setSnaps([]));
    portalApi
      .runs(plan.id)
      .then(setRuns)
      .catch(() => setRuns([]));
  }, [plan.id]);
  return (
    <Stack gap={4} p={3} bg="bg.muted" borderRadius="md">
      <Box>
        <Text fontSize="sm">
          Paths: <Code fontSize="xs">{plan.paths.join(", ")}</Code>
        </Text>
        <Text fontSize="sm">
          Schedule: {plan.schedule}
          {plan.retention ? ` · ${plan.retention}` : ""}
        </Text>
      </Box>
      <Box>
        <Text fontWeight="semibold" fontSize="sm" mb={1}>
          Recent runs
        </Text>
        {runs === null ? (
          <Text fontSize="sm">Loading…</Text>
        ) : runs.length === 0 ? (
          <Text fontSize="sm" color="fg.muted">
            No runs yet.
          </Text>
        ) : (
          <Box overflowX="auto">
            <Table.Root size="sm">
              <Table.Body>
                {runs.map((r) => (
                  <Table.Row key={r.operationId}>
                    <Table.Cell whiteSpace="nowrap">
                      {formatTime(r.startedAt)}
                    </Table.Cell>
                    <Table.Cell>
                      <Badge colorPalette={runColor[r.status] || "gray"}>
                        {runLabel[r.status] || r.status}
                      </Badge>
                    </Table.Cell>
                    <Table.Cell>
                      {r.bytesTotal
                        ? `${formatBytes(r.bytesTotal)} (+${formatBytes(r.bytesAdded)})`
                        : ""}
                    </Table.Cell>
                    <Table.Cell fontSize="xs" color="fg.muted">
                      {r.message}
                    </Table.Cell>
                  </Table.Row>
                ))}
              </Table.Body>
            </Table.Root>
          </Box>
        )}
      </Box>
      <Box>
        <Text fontWeight="semibold" fontSize="sm" mb={1}>
          Snapshots ({snaps?.length ?? "…"})
        </Text>
        {snaps && snaps.length > 0 && (
          <Box overflowX="auto" maxH="240px" overflowY="auto">
            <Table.Root size="sm">
              <Table.Body>
                {snaps.slice(0, 100).map((s) => (
                  <Table.Row key={s.id}>
                    <Table.Cell whiteSpace="nowrap">
                      {formatTime(s.time)}
                    </Table.Cell>
                    <Table.Cell>
                      <Code fontSize="xs">{s.id.slice(0, 8)}</Code>
                    </Table.Cell>
                    <Table.Cell>
                      {s.bytes ? formatBytes(s.bytes) : ""}
                    </Table.Cell>
                  </Table.Row>
                ))}
              </Table.Body>
            </Table.Root>
          </Box>
        )}
      </Box>
    </Stack>
  );
};

const Plans = ({
  ov,
  admin,
  reload,
}: {
  ov: Overview;
  admin: boolean;
  reload: () => void;
}) => {
  const [open, setOpen] = useState<string | null>(null);
  const backup = async (id: string) => {
    try {
      await portalApi.backup(id);
      alerts.success("Backup started");
      setTimeout(reload, 1500);
    } catch (e: any) {
      alerts.error(e.message);
    }
  };
  if (ov.plans.length === 0) {
    return (
      <Text color="fg.muted">
        No backup plans yet. Your provider sets them up for you.
      </Text>
    );
  }
  return (
    <Stack gap={2}>
      {ov.plans.map((p) => (
        <Box
          key={p.id}
          borderWidth="1px"
          borderRadius="md"
          p={3}
          data-testid={`plan-${p.id}`}
        >
          <Flex gap={3} align="center" wrap="wrap">
            <Box flex="1" minW="180px">
              <Flex gap={2} align="center" wrap="wrap">
                <Text fontWeight="semibold">{p.id}</Text>
                <StateBadge state={p.state} />
                {p.ownStorage && (
                  <Badge colorPalette="purple" variant="outline">
                    own storage
                  </Badge>
                )}
              </Flex>
              <Text fontSize="sm" color="fg.muted">
                Last successful backup: {formatTime(p.lastSuccessAt)} ·{" "}
                {p.snapshots} snapshot(s) · {p.repo}
              </Text>
            </Box>
            <Flex gap={2}>
              <Button
                size="sm"
                variant="outline"
                onClick={() => setOpen(open === p.id ? null : p.id)}
              >
                {open === p.id ? "Hide" : "Details"}
              </Button>
              {admin && (
                <Button
                  size="sm"
                  onClick={() => backup(p.id)}
                  disabled={p.state === "running"}
                  data-testid={`backup-${p.id}`}
                >
                  Back up now
                </Button>
              )}
            </Flex>
          </Flex>
          {open === p.id && (
            <Box mt={3}>
              <PlanDetails plan={p} />
            </Box>
          )}
        </Box>
      ))}
    </Stack>
  );
};

// ---- storage and recovery kits ----

const download = (name: string, text: string) => {
  const blob = new Blob([text], { type: "text/plain" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
};

const KitButton = ({
  repo,
  escrow,
  reload,
}: {
  repo: RepoView;
  escrow: boolean;
  reload: () => void;
}) => {
  const [busy, setBusy] = useState(false);
  const [kit, setKit] = useState<string | null>(null);
  const create = async () => {
    if (
      repo.kit &&
      !window.confirm(
        "A new recovery kit replaces the current one: the key in your current kit stops working. Continue?",
      )
    ) {
      return;
    }
    setBusy(true);
    try {
      const text = await portalApi.createKit(repo.id);
      setKit(text);
      download(`relyper-recovery-kit-${repo.id}.txt`, text);
      reload();
    } catch (e: any) {
      alerts.error(e.message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Stack gap={2}>
      <Button
        size="sm"
        variant="outline"
        loading={busy}
        onClick={create}
        data-testid={`kit-${repo.id}`}
      >
        {repo.kit ? "Create new recovery kit" : "Create recovery kit"}
      </Button>
      {kit && (
        <Box
          borderWidth="1px"
          borderColor="orange.400"
          borderRadius="md"
          p={2}
          data-testid="kit-shown"
        >
          <Text fontSize="sm" mb={1}>
            Your recovery kit was downloaded. It is shown only now; Relyper
            Backup does not keep the key
            {escrow ? " (your provider keeps a sealed copy)" : ""}.
          </Text>
          <Flex gap={2}>
            <Button
              size="xs"
              onClick={() =>
                download(`relyper-recovery-kit-${repo.id}.txt`, kit)
              }
            >
              Download again
            </Button>
            <Button size="xs" variant="ghost" onClick={() => setKit(null)}>
              Done, I stored it
            </Button>
          </Flex>
        </Box>
      )}
    </Stack>
  );
};

const emptyStorage: StorageRequest = {
  name: "",
  type: "s3",
  existing: false,
};

const AddStorage = ({ onDone }: { onDone: () => void }) => {
  const [s, setS] = useState<StorageRequest>(emptyStorage);
  const [busy, setBusy] = useState(false);
  const set = (k: keyof StorageRequest, v: any) => setS({ ...s, [k]: v });
  const field = (
    k: keyof StorageRequest,
    label: string,
    placeholder?: string,
    type = "text",
  ) => (
    <Field label={label}>
      <Input
        size="sm"
        type={type}
        value={(s[k] as string) || ""}
        placeholder={placeholder}
        onChange={(e) => set(k, e.target.value)}
        data-testid={`storage-${k}`}
      />
    </Field>
  );
  const submit = async () => {
    setBusy(true);
    try {
      await portalApi.addStorage(s);
      alerts.success("Storage connected");
      setS(emptyStorage);
      onDone();
    } catch (e: any) {
      alerts.error(e.message);
    } finally {
      setBusy(false);
    }
  };
  const keyLabels: Record<string, [string, string]> = {
    s3: ["Access key ID", "Secret access key"],
    b2: ["Account ID / key ID", "Application key"],
    azure: ["Account name", "Account key"],
    rest: ["User (optional)", "Password (optional)"],
  };
  return (
    <Stack gap={3} data-testid="add-storage">
      <Flex gap={3} wrap="wrap">
        <Box flex="1" minW="160px">
          {field("name", "Name", "e.g. my-s3")}
        </Box>
        <Field label="Type" flex="1" minW="160px">
          <NativeSelect.Root size="sm">
            <NativeSelect.Field
              value={s.type}
              onChange={(e) => set("type", e.target.value)}
              data-testid="storage-type"
            >
              <option value="s3">S3 / S3-compatible</option>
              <option value="b2">Backblaze B2</option>
              <option value="azure">Azure Blob</option>
              <option value="rest">REST server</option>
            </NativeSelect.Field>
          </NativeSelect.Root>
        </Field>
      </Flex>
      <Flex gap={3} wrap="wrap">
        {(s.type === "s3" || s.type === "rest") && (
          <Box flex="2" minW="200px">
            {field(
              "endpoint",
              s.type === "s3" ? "Endpoint (https)" : "URL (https)",
              s.type === "s3"
                ? "https://s3.eu-central-1.amazonaws.com"
                : "https://backup.example.com/repo",
            )}
          </Box>
        )}
        {s.type !== "rest" && (
          <Box flex="1" minW="140px">
            {field("bucket", s.type === "azure" ? "Container" : "Bucket")}
          </Box>
        )}
        {s.type !== "rest" && (
          <Box flex="1" minW="140px">
            {field("path", "Path (optional)", "relyper")}
          </Box>
        )}
        {s.type === "s3" && (
          <Box flex="1" minW="120px">
            {field("region", "Region (optional)", "eu-central-1")}
          </Box>
        )}
      </Flex>
      <Flex gap={3} wrap="wrap">
        <Box flex="1" minW="160px">
          {field("keyId", keyLabels[s.type][0])}
        </Box>
        <Box flex="1" minW="160px">
          {field("secretKey", keyLabels[s.type][1], "", "password")}
        </Box>
      </Flex>
      <Checkbox
        checked={s.existing}
        onCheckedChange={(e: any) => set("existing", !!e.checked)}
      >
        <Text fontSize="sm">
          This is an existing restic repository (enter its password)
        </Text>
      </Checkbox>
      {s.existing && field("password", "Repository password", "", "password")}
      <Text fontSize="xs" color="fg.muted">
        A new repository gets a strong key generated by the server. Create a
        recovery kit afterwards so you hold your own key.
      </Text>
      <Box>
        <Button
          size="sm"
          loading={busy}
          onClick={submit}
          data-testid="storage-submit"
        >
          Connect storage
        </Button>
      </Box>
    </Stack>
  );
};

const CopyPlan = ({
  ov,
  repo,
  onDone,
}: {
  ov: Overview;
  repo: RepoView;
  onDone: () => void;
}) => {
  const sources = ov.plans.filter((p) => p.repo !== repo.id && !p.ownStorage);
  const [plan, setPlan] = useState(sources[0]?.id || "");
  const [name, setName] = useState("");
  if (sources.length === 0) return null;
  const submit = async () => {
    try {
      await portalApi.copyPlan(repo.id, plan, name || `${plan}-own`);
      alerts.success("Plan copied to your storage");
      onDone();
    } catch (e: any) {
      alerts.error(e.message);
    }
  };
  return (
    <Flex gap={2} align="end" wrap="wrap">
      <Field label="Also back up plan" flex="1" minW="140px">
        <NativeSelect.Root size="sm">
          <NativeSelect.Field
            value={plan}
            onChange={(e) => setPlan(e.target.value)}
          >
            {sources.map((p) => (
              <option key={p.id} value={p.id}>
                {p.id}
              </option>
            ))}
          </NativeSelect.Field>
        </NativeSelect.Root>
      </Field>
      <Field label="Name of the copy" flex="1" minW="140px">
        <Input
          size="sm"
          value={name}
          placeholder={`${plan}-own`}
          onChange={(e) => setName(e.target.value)}
        />
      </Field>
      <Button size="sm" variant="outline" onClick={submit}>
        Copy plan
      </Button>
    </Flex>
  );
};

const Storage = ({
  ov,
  me,
  reload,
}: {
  ov: Overview;
  me: Me;
  reload: () => void;
}) => {
  const [adding, setAdding] = useState(false);
  const remove = async (id: string) => {
    if (
      !window.confirm(
        `Disconnect ${id}? Plans that write to it are removed. Data in the storage is not deleted.`,
      )
    )
      return;
    try {
      await portalApi.removeStorage(id);
      alerts.success("Storage disconnected");
      reload();
    } catch (e: any) {
      alerts.error(e.message);
    }
  };
  return (
    <Stack gap={3}>
      {ov.repos.map((r) => (
        <Box
          key={r.id}
          borderWidth="1px"
          borderRadius="md"
          p={3}
          data-testid={`repo-${r.id}`}
        >
          <Flex gap={3} wrap="wrap" align="start">
            <Box flex="1" minW="200px">
              <Flex gap={2} align="center" wrap="wrap">
                <Text fontWeight="semibold">{r.id}</Text>
                <Badge>{r.type}</Badge>
                {r.own ? (
                  <Badge colorPalette="purple">your storage</Badge>
                ) : (
                  <Badge colorPalette="blue">provider storage</Badge>
                )}
              </Flex>
              <Text fontSize="xs" color="fg.muted" wordBreak="break-all">
                {r.location}
              </Text>
              <Text fontSize="sm" mt={1}>
                {r.kit
                  ? `Recovery kit created ${new Date(r.kit.createdAt).toLocaleString()} (key ${r.kit.keyId.slice(0, 8)})${r.kit.escrowed ? ", sealed copy held by your provider" : ""}`
                  : "No recovery kit yet: create one so you hold your own key."}
              </Text>
            </Box>
            {me.admin && (
              <Stack gap={2} align="stretch">
                {me.features.kits && (
                  <KitButton
                    repo={r}
                    escrow={ov.tenant.escrow}
                    reload={reload}
                  />
                )}
                {r.own && (
                  <Button
                    size="sm"
                    variant="ghost"
                    colorPalette="red"
                    onClick={() => remove(r.id)}
                  >
                    Disconnect
                  </Button>
                )}
              </Stack>
            )}
          </Flex>
          {me.admin && r.own && (
            <Box mt={3}>
              <CopyPlan ov={ov} repo={r} onDone={reload} />
            </Box>
          )}
        </Box>
      ))}
      {me.admin && me.features.storage && (
        <Box>
          {adding ? (
            <Box borderWidth="1px" borderRadius="md" p={3}>
              <AddStorage
                onDone={() => {
                  setAdding(false);
                  reload();
                }}
              />
            </Box>
          ) : (
            <Button
              size="sm"
              variant="outline"
              onClick={() => setAdding(true)}
              data-testid="storage-add"
            >
              Connect your own storage
            </Button>
          )}
        </Box>
      )}
      <Text fontSize="xs" color="fg.muted">
        {ov.tenant.escrow
          ? "Key escrow is booked: when you create a recovery kit, your provider keeps a sealed copy of the key that only they can open offline."
          : "Without key escrow nobody but you has your recovery kit. Key escrow can be booked with your provider."}
      </Text>
    </Stack>
  );
};

// ---- notifications ----

const Notifications = ({ me }: { me: Me }) => {
  const [n, setN] = useState<Notify | null>(null);
  const [events, setEvents] = useState<string[]>([]);
  const [available, setAvailable] = useState(false);
  const [text, setText] = useState("");
  useEffect(() => {
    portalApi.notifications().then((r) => {
      setN(r.notify);
      setEvents(r.events);
      setAvailable(r.available);
      setText((r.notify.recipients || []).join("\n"));
    });
  }, []);
  if (!n) return <Text>Loading…</Text>;
  const save = async () => {
    try {
      const recipients = text
        .split(/[\n,;]+/)
        .map((s) => s.trim())
        .filter(Boolean);
      const r = await portalApi.setNotifications({
        recipients,
        events: n.events,
      });
      setN(r.notify);
      alerts.success("Saved");
    } catch (e: any) {
      alerts.error(e.message);
    }
  };
  const test = async () => {
    try {
      await portalApi.testMail();
      alerts.success("Test mail sent");
    } catch (e: any) {
      alerts.error(e.message);
    }
  };
  return (
    <Stack gap={3} data-testid="notifications">
      {!available && (
        <Text fontSize="sm" color="orange.500">
          E-mail is not set up on this server yet; settings are kept but no
          mails are sent.
        </Text>
      )}
      <Field label="Recipients (one per line, up to 10)">
        <textarea
          value={text}
          rows={3}
          disabled={!me.admin}
          onChange={(e) => setText(e.target.value)}
          style={{
            width: "100%",
            padding: 8,
            borderWidth: 1,
            borderRadius: 6,
            fontSize: 14,
            background: "transparent",
          }}
          data-testid="notify-recipients"
        />
      </Field>
      <Stack gap={1}>
        {events.map((ev) => (
          <Checkbox
            key={ev}
            disabled={!me.admin}
            checked={(n.events || []).includes(ev)}
            onCheckedChange={(e: any) =>
              setN({
                ...n,
                events: e.checked
                  ? [...(n.events || []), ev]
                  : (n.events || []).filter((x) => x !== ev),
              })
            }
          >
            <Text fontSize="sm">{eventLabel[ev] || ev}</Text>
          </Checkbox>
        ))}
      </Stack>
      {me.admin && (
        <Flex gap={2}>
          <Button size="sm" onClick={save} data-testid="notify-save">
            Save
          </Button>
          <Button
            size="sm"
            variant="outline"
            onClick={test}
            disabled={!available}
          >
            Send test mail
          </Button>
        </Flex>
      )}
    </Stack>
  );
};

// ---- page ----

export const PortalHome = () => {
  const [me, setMe] = useState<Me | null>(null);
  const [ov, setOv] = useState<Overview | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const [m, o] = await Promise.all([portalApi.me(), portalApi.overview()]);
      setMe(m);
      setOv(o);
      setError(null);
    } catch (e: any) {
      if (e instanceof PortalError && e.status === 401) {
        setPortalToken(null);
        window.location.reload();
        return;
      }
      setError(e.message);
    }
  }, []);

  useEffect(() => {
    load();
    const t = setInterval(load, 30000);
    return () => clearInterval(t);
  }, [load]);

  if (error) {
    return (
      <Container maxW="4xl" py={8}>
        <Text color="red.500">{error}</Text>
      </Container>
    );
  }
  if (!me || !ov) {
    return (
      <Container maxW="4xl" py={8}>
        <Text>Loading…</Text>
      </Container>
    );
  }
  return (
    <Container maxW="5xl" py={6} px={4}>
      <Flex align="center" gap={3} mb={6} wrap="wrap">
        <img src={IconSvg} alt="" style={{ height: 28 }} />
        <Box flex="1" minW="160px">
          <Heading size="md" data-testid="portal-tenant">
            {ov.tenant.name}
          </Heading>
          <Text fontSize="xs" color="fg.muted">
            Relyper Backup
          </Text>
        </Box>
        <UserMenuView
          title={me.user.name || me.user.email}
          subtitle={me.user.name ? me.user.email : undefined}
          rows={[
            { label: "Sign-in", value: "Relyper" },
            { label: "Company", value: me.tenant.name },
            {
              label: "Role",
              value: me.admin ? "Administrator" : "Viewer",
            },
          ]}
          rights={
            me.admin
              ? "You can see your backups, start backups, change storage and notifications and create recovery kits."
              : "You can see the state of your backups. Ask an administrator of your company for changes."
          }
          badge={me.admin ? undefined : { label: "Viewer", color: "gray" }}
          onSignOut={() => {
            setPortalToken(null);
            window.location.reload();
          }}
        />
      </Flex>

      {ov.problems.length > 0 && (
        <SectionCard title="Needs attention">
          <Stack gap={2} data-testid="problems">
            {ov.problems.map((p) => (
              <Flex key={p.plan + p.state} gap={2} align="center" wrap="wrap">
                <StateBadge state={p.state} />
                <Text fontSize="sm">
                  <b>{p.plan}</b>: {p.message}
                </Text>
              </Flex>
            ))}
          </Stack>
        </SectionCard>
      )}
      <SectionCard title="Backups">
        <Plans ov={ov} admin={me.admin} reload={load} />
      </SectionCard>
      <SectionCard
        title="Storage and recovery kits"
        description="Your recovery kit holds your own key: with it you can restore with the plain restic CLI, without us."
      >
        <Storage ov={ov} me={me} reload={load} />
      </SectionCard>
      <SectionCard title="E-mail notifications">
        <Notifications me={me} />
      </SectionCard>
    </Container>
  );
};

export const PortalApp = () => {
  const [phase, setPhase] = useState<"init" | "signin" | "home">("init");
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    const code = params.get("relyper_login_error");
    if (code) {
      clearLoginQuery();
      setError(loginErrors[code] || "The sign-in failed. Try again.");
      setPhase("signin");
      return;
    }
    if (params.get("relyper_login") === "1") {
      completePortalHandoff()
        .then((kind) => {
          clearLoginQuery();
          if (kind === "operator") {
            window.location.assign(window.location.pathname + "#/");
            window.location.reload();
          } else if (kind === "portal") {
            setPhase("home");
          } else {
            setError("The sign-in expired. Try again.");
            setPhase("signin");
          }
        })
        .catch(() => {
          clearLoginQuery();
          setError("The sign-in failed. Try again.");
          setPhase("signin");
        });
      return;
    }
    setPhase(getPortalToken() ? "home" : "signin");
  }, []);

  return (
    <>
      {phase === "signin" && <SignIn error={error} />}
      {phase === "home" && <PortalHome />}
    </>
  );
};

export default PortalApp;
