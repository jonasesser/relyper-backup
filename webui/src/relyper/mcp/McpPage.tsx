// relyper: MCP integration page – connection details, approval of restricted
// operations requested by AI clients, access tokens and the audit log.
//
// Approvals happen only here, in the signed-in web UI. MCP tokens cannot call
// the RelyperMcp service, so an AI client can never approve its own request.
import React, { useCallback, useEffect, useState } from "react";
import {
  Badge,
  Box,
  Code,
  Flex,
  Heading,
  Input,
  Stack,
  Table,
  Text,
} from "@chakra-ui/react";
import { create } from "@bufbuild/protobuf";
import {
  Approval,
  AuditEvent,
  CreateTokenRequestSchema,
  DecideApprovalRequestSchema,
  ListApprovalsRequestSchema,
  ListAuditEventsRequestSchema,
  ListTokensRequestSchema,
  GetMcpStatusRequestSchema,
  McpStatus,
  McpToken,
  RevokeTokenRequestSchema,
} from "../../../gen/ts/relyper/v1/mcp_pb";
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
import { alerts, formatErrorAlert } from "../../components/common/Alerts";
import { relyperMcpService } from "../api";

const fmtTime = (ms: bigint | number) => {
  const n = Number(ms);
  return n ? new Date(n).toLocaleString() : "–";
};

const statusColor: Record<string, string> = {
  pending: "orange",
  approved: "blue",
  used: "green",
  denied: "red",
  expired: "gray",
};

export const endpointUrl = (status: McpStatus | null, origin: string) =>
  (status?.publicUrl || origin).replace(/\/$/, "") +
  (status?.endpointPath || "/mcp");

const CopyButton = ({ text, label }: { text: string; label?: string }) => (
  <Button
    size="xs"
    variant="outline"
    onClick={() => {
      navigator.clipboard?.writeText(text).then(
        () => alerts.success("Copied"),
        () => alerts.error("Copy failed"),
      );
    }}
  >
    {label || "Copy"}
  </Button>
);

// Users signed in through the identity provider need the admin role to
// decide approvals and manage tokens; local users always may.
const NeedsAdmin = ({
  status,
  what,
}: {
  status: McpStatus | null;
  what: string;
}) => (
  <Text fontSize="sm" color="fg.muted" data-testid="mcp-needs-admin">
    Only users with the role{" "}
    <Code fontSize="xs">{status?.adminRole || "admin"}</Code> can {what}.
  </Text>
);

const ConnectSection = ({ status }: { status: McpStatus | null }) => {
  const url = endpointUrl(status, window.location.origin);
  const claudeCode = `claude mcp add --transport http relyper-backup ${url} --header "Authorization: Bearer <token>"`;
  return (
    <SectionCard title="Connect an AI client">
      <Stack gap={3} fontSize="sm">
        <Flex gap={2} align="center" wrap="wrap">
          <Text fontWeight="medium">MCP endpoint:</Text>
          <Code data-testid="mcp-endpoint" wordBreak="break-all">
            {url}
          </Code>
          <CopyButton text={url} />
        </Flex>
        <Text>
          <strong>
            Claude, ChatGPT and other apps with remote connectors:
          </strong>{" "}
          add a custom connector with this URL. You will be asked to sign in
          {status?.identityProvider
            ? ` with ${status.identityProvider} or a local Relyper Backup user`
            : " with your Relyper Backup user"}{" "}
          and choose the permissions. The server must be reachable over HTTPS
          from the app.
        </Text>
        <Text>
          <strong>Clients with header configuration</strong> (Claude Code,
          Claude Desktop config, scripts): create an access token below and send
          it as <Code>Authorization: Bearer &lt;token&gt;</Code>.
        </Text>
        <Flex gap={2} align="center" wrap="wrap">
          <Code fontSize="xs" whiteSpace="pre-wrap" wordBreak="break-all">
            {claudeCode}
          </Code>
          <CopyButton text={claudeCode} />
        </Flex>
        {!status?.publicUrl && (
          <Text color="fg.muted">
            The URL is derived from the address you use right now. Behind a
            reverse proxy, set <Code>RELYPER_PUBLIC_URL</Code> (e.g.
            https://backup.example.com) so OAuth clients get the right address.
          </Text>
        )}
        {status && !status.loginAvailable && !status.identityProvider && (
          <Text color="red.500">
            No user account exists, so OAuth sign-in is not possible. Create a
            user in the settings first.
          </Text>
        )}
        <Text color="fg.muted">
          Restore, deleting snapshots or repositories, pruning, retention
          changes and trusting SFTP host keys are never executed directly: the
          AI client gets a preview, and you approve each request on this page.
        </Text>
      </Stack>
    </SectionCard>
  );
};

const ApprovalCard = ({
  a,
  status,
  onDecided,
}: {
  a: Approval;
  status: McpStatus | null;
  onDecided: () => void;
}) => {
  const [busy, setBusy] = useState(false);
  const [showDetails, setShowDetails] = useState(a.status === "pending");
  const decide = async (approve: boolean) => {
    setBusy(true);
    try {
      await relyperMcpService.decideApproval(
        create(DecideApprovalRequestSchema, { id: a.id, approve }),
      );
      alerts.success(approve ? "Approved" : "Denied");
      onDecided();
    } catch (e: any) {
      alerts.error(formatErrorAlert(e, "Decision failed: "));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Box
      borderWidth="1px"
      borderColor={a.status === "pending" ? "orange.400" : "border"}
      borderRadius="md"
      p={3}
      data-testid={`approval-${a.id}`}
    >
      <Flex justify="space-between" gap={2} wrap="wrap" align="center">
        <Box minW={0}>
          <Text fontWeight="semibold" wordBreak="break-word">
            {a.summary}
          </Text>
          <Text fontSize="xs" color="fg.muted">
            <Code fontSize="xs">{a.tool}</Code> · requested by {a.tokenName} ·{" "}
            {fmtTime(a.createdAtMs)}
            {a.status === "pending" && <> · expires {fmtTime(a.expiresAtMs)}</>}
            {a.decidedBy && <> · decided by {a.decidedBy}</>}
            {a.operationId ? <> · operation {String(a.operationId)}</> : null}
          </Text>
        </Box>
        <Badge colorPalette={statusColor[a.status] || "gray"}>{a.status}</Badge>
      </Flex>
      <Button
        size="xs"
        variant="ghost"
        mt={1}
        px={0}
        onClick={() => setShowDetails(!showDetails)}
      >
        {showDetails ? "Hide details" : "Show details"}
      </Button>
      {showDetails && (
        <Stack gap={2} mt={1}>
          <Text fontSize="xs" fontWeight="medium">
            Preview
          </Text>
          <Code
            as="pre"
            fontSize="xs"
            p={2}
            whiteSpace="pre-wrap"
            wordBreak="break-all"
            maxH="240px"
            overflowY="auto"
          >
            {a.previewJson}
          </Code>
          <Text fontSize="xs" fontWeight="medium">
            Exact parameters
          </Text>
          <Code fontSize="xs" p={2} whiteSpace="pre-wrap" wordBreak="break-all">
            {a.paramsJson}
          </Code>
        </Stack>
      )}
      {a.status === "pending" && !status?.canAdminister && (
        <Box mt={3}>
          <NeedsAdmin status={status} what="approve or deny requests" />
        </Box>
      )}
      {a.status === "pending" && status?.canAdminister && (
        <Flex gap={2} mt={3}>
          <Button
            size="sm"
            colorPalette="red"
            variant="outline"
            loading={busy}
            onClick={() => decide(false)}
          >
            Deny
          </Button>
          <Button
            size="sm"
            colorPalette="blue"
            loading={busy}
            onClick={() => decide(true)}
            data-testid="approval-approve"
          >
            Approve
          </Button>
        </Flex>
      )}
    </Box>
  );
};

const ApprovalsSection = ({
  approvals,
  status,
  reload,
}: {
  approvals: Approval[];
  status: McpStatus | null;
  reload: () => void;
}) => {
  const pending = approvals.filter((a) => a.status === "pending");
  const done = approvals.filter((a) => a.status !== "pending");
  return (
    <SectionCard
      title="Approval requests"
      description="Requests expire after 15 minutes. An approval is valid once, for exactly the parameters shown."
    >
      <Stack gap={3}>
        {pending.length === 0 && (
          <Text fontSize="sm" color="fg.muted">
            No request is waiting for a decision.
          </Text>
        )}
        {pending.map((a) => (
          <ApprovalCard key={a.id} a={a} status={status} onDecided={reload} />
        ))}
        {done.length > 0 && (
          <Text fontSize="sm" fontWeight="medium" pt={2}>
            Recent decisions
          </Text>
        )}
        {done.slice(0, 20).map((a) => (
          <ApprovalCard key={a.id} a={a} status={status} onDecided={reload} />
        ))}
      </Stack>
    </SectionCard>
  );
};

const NewTokenForm = ({
  status,
  onCreated,
}: {
  status: McpStatus | null;
  onCreated: () => void;
}) => {
  const [name, setName] = useState("");
  const [days, setDays] = useState(90);
  const [scopes, setScopes] = useState<string[]>(["status:read"]);
  const [secret, setSecret] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    setBusy(true);
    try {
      const resp = await relyperMcpService.createToken(
        create(CreateTokenRequestSchema, {
          name,
          scopes,
          lifetimeDays: days,
        }),
      );
      setSecret(resp.secret);
      setName("");
      onCreated();
    } catch (e: any) {
      alerts.error(formatErrorAlert(e, "Creating the token failed: "));
    } finally {
      setBusy(false);
    }
  };

  if (secret) {
    return (
      <Box
        borderWidth="1px"
        borderColor="green.400"
        borderRadius="md"
        p={3}
        data-testid="mcp-token-secret"
      >
        <Text fontWeight="semibold">Copy the token now</Text>
        <Text fontSize="sm" color="fg.muted">
          It is shown only once and cannot be recovered.
        </Text>
        <Flex gap={2} mt={2} align="center" wrap="wrap">
          <Code wordBreak="break-all">{secret}</Code>
          <CopyButton text={secret} />
        </Flex>
        <Button size="sm" mt={3} onClick={() => setSecret(null)}>
          Done
        </Button>
      </Box>
    );
  }

  return (
    <Stack gap={3}>
      <Flex gap={3} wrap="wrap">
        <Field label="Name" flex="1" minW="200px">
          <Input
            value={name}
            placeholder="e.g. Claude Code on laptop"
            onChange={(e) => setName(e.target.value)}
            data-testid="mcp-token-name"
          />
        </Field>
        <Field label="Valid for (days)" w="140px">
          <Input
            type="number"
            min={1}
            max={366}
            value={days}
            onChange={(e) => setDays(Number(e.target.value))}
          />
        </Field>
      </Flex>
      <Stack gap={1}>
        <Text fontSize="sm" fontWeight="medium">
          Permissions
        </Text>
        {(status?.scopes || []).map((s) => (
          <Checkbox
            key={s.name}
            checked={scopes.includes(s.name)}
            onCheckedChange={(e: any) =>
              setScopes(
                e.checked
                  ? [...scopes, s.name]
                  : scopes.filter((x) => x !== s.name),
              )
            }
          >
            <Text fontSize="sm">
              <Code fontSize="xs">{s.name}</Code> {s.description}
              {s.restricted && (
                <Badge ml={1} colorPalette="orange" size="sm">
                  needs approval
                </Badge>
              )}
            </Text>
          </Checkbox>
        ))}
      </Stack>
      <Box>
        <Button
          size="sm"
          colorPalette="blue"
          loading={busy}
          disabled={!name.trim() || scopes.length === 0}
          onClick={submit}
          data-testid="mcp-token-create"
        >
          Create token
        </Button>
      </Box>
    </Stack>
  );
};

const TokensSection = ({
  status,
  tokens,
  reload,
}: {
  status: McpStatus | null;
  tokens: McpToken[];
  reload: () => void;
}) => {
  const revoke = async (t: McpToken) => {
    if (!window.confirm(`Revoke "${t.name}"? Clients using it lose access.`))
      return;
    try {
      await relyperMcpService.revokeToken(
        create(RevokeTokenRequestSchema, { id: t.id }),
      );
      reload();
    } catch (e: any) {
      alerts.error(formatErrorAlert(e, "Revoking failed: "));
    }
  };
  const visible = tokens.filter((t) => t.active || t.revokedAtMs);
  return (
    <>
      <SectionCard title="New access token">
        {status?.canAdminister ? (
          <NewTokenForm status={status} onCreated={reload} />
        ) : (
          <NeedsAdmin status={status} what="create tokens" />
        )}
      </SectionCard>
      <SectionCard
        title="Tokens and connected apps"
        description="Apps connected with OAuth appear here too. Revoking takes effect immediately."
      >
        {visible.length === 0 ? (
          <Text fontSize="sm" color="fg.muted">
            No tokens yet.
          </Text>
        ) : (
          <Box overflowX="auto">
            <Table.Root size="sm">
              <Table.Header>
                <Table.Row>
                  <Table.ColumnHeader>Name</Table.ColumnHeader>
                  <Table.ColumnHeader>Permissions</Table.ColumnHeader>
                  <Table.ColumnHeader>Last used</Table.ColumnHeader>
                  <Table.ColumnHeader>Expires</Table.ColumnHeader>
                  <Table.ColumnHeader></Table.ColumnHeader>
                </Table.Row>
              </Table.Header>
              <Table.Body>
                {visible.map((t) => (
                  <Table.Row key={t.id}>
                    <Table.Cell>
                      <Text fontWeight="medium">{t.name}</Text>
                      <Text fontSize="xs" color="fg.muted">
                        {t.kind === "oauth" ? "OAuth app" : "Access token"} ·{" "}
                        {t.createdBy}
                      </Text>
                    </Table.Cell>
                    <Table.Cell>
                      <Text fontSize="xs">{t.scopes.join(", ")}</Text>
                    </Table.Cell>
                    <Table.Cell>{fmtTime(t.lastUsedAtMs)}</Table.Cell>
                    <Table.Cell>
                      {fmtTime(
                        t.kind === "oauth"
                          ? t.refreshExpiresAtMs
                          : t.expiresAtMs,
                      )}
                    </Table.Cell>
                    <Table.Cell>
                      {t.revokedAtMs ? (
                        <Badge>revoked</Badge>
                      ) : !status?.canAdminister ? null : (
                        <Button
                          size="xs"
                          variant="outline"
                          colorPalette="red"
                          onClick={() => revoke(t)}
                        >
                          Revoke
                        </Button>
                      )}
                    </Table.Cell>
                  </Table.Row>
                ))}
              </Table.Body>
            </Table.Root>
          </Box>
        )}
      </SectionCard>
    </>
  );
};

const AuditSection = ({ events }: { events: AuditEvent[] }) => (
  <SectionCard
    title="Audit log"
    description="MCP tool calls, approvals, token and OAuth events. Secrets are never written."
  >
    {events.length === 0 ? (
      <Text fontSize="sm" color="fg.muted">
        Nothing recorded yet.
      </Text>
    ) : (
      <Stack gap={0} fontSize="xs">
        {events.map((e, i) => (
          <Box key={i} py={1.5} borderTopWidth={i ? "1px" : 0}>
            <Flex gap={2} wrap="wrap" align="center">
              <Text color="fg.muted">{fmtTime(e.timeMs)}</Text>
              <Code fontSize="xs">{e.action}</Code>
              <Badge
                size="sm"
                colorPalette={
                  e.outcome === "ok"
                    ? "green"
                    : e.outcome === "approval_requested" ||
                        e.outcome === "approved"
                      ? "orange"
                      : e.outcome === "executing"
                        ? "blue"
                        : "red"
                }
              >
                {e.outcome}
              </Badge>
              <Text>{e.actor}</Text>
            </Flex>
            {(e.message || e.params) && (
              <Text color="fg.muted" wordBreak="break-all">
                {e.message}
                {e.message && e.params ? " · " : ""}
                {e.params}
              </Text>
            )}
          </Box>
        ))}
      </Stack>
    )}
  </SectionCard>
);

const ToolsSection = ({ status }: { status: McpStatus | null }) => {
  const byScope: Record<string, McpStatus["tools"]> = {};
  (status?.tools || []).forEach((t) => {
    (byScope[t.scope] = byScope[t.scope] || []).push(t);
  });
  return (
    <SectionCard
      title="Available tools"
      description="A client only sees the tools its permissions allow."
    >
      <Stack gap={3} fontSize="sm">
        {(status?.scopes || []).map((s) => (
          <Box key={s.name}>
            <Text fontWeight="medium">
              <Code fontSize="xs">{s.name}</Code>
            </Text>
            <Flex gap={1} wrap="wrap" mt={1}>
              {(byScope[s.name] || []).map((t) => (
                <Badge
                  key={t.name}
                  variant="subtle"
                  colorPalette={t.restricted ? "orange" : "gray"}
                  title={t.description}
                >
                  {t.name}
                </Badge>
              ))}
            </Flex>
          </Box>
        ))}
      </Stack>
    </SectionCard>
  );
};

export const McpPage = () => {
  const [status, setStatus] = useState<McpStatus | null>(null);
  const [tokens, setTokens] = useState<McpToken[]>([]);
  const [approvals, setApprovals] = useState<Approval[]>([]);
  const [events, setEvents] = useState<AuditEvent[]>([]);
  const [tab, setTab] = useState("approvals");
  const [error, setError] = useState<React.ReactNode>(null);

  const reload = useCallback(async () => {
    try {
      const [s, t, a, e] = await Promise.all([
        relyperMcpService.getMcpStatus(create(GetMcpStatusRequestSchema, {})),
        relyperMcpService.listTokens(create(ListTokensRequestSchema, {})),
        relyperMcpService.listApprovals(create(ListApprovalsRequestSchema, {})),
        relyperMcpService.listAuditEvents(
          create(ListAuditEventsRequestSchema, { limit: 200 }),
        ),
      ]);
      setStatus(s);
      setTokens(t.tokens);
      setApprovals(a.approvals);
      setEvents(e.events);
      setError(null);
    } catch (e: any) {
      setError(formatErrorAlert(e, "MCP status unavailable: "));
    }
  }, []);

  useEffect(() => {
    reload();
    // pending approvals should show up without a manual refresh
    const timer = setInterval(reload, 5000);
    return () => clearInterval(timer);
  }, [reload]);

  const pending = approvals.filter((a) => a.status === "pending").length;

  return (
    <Box maxW="1000px" data-testid="mcp-page">
      <Heading size="lg" mb={1}>
        MCP Integration
      </Heading>
      <Text color="fg.muted" mb={4} fontSize="sm">
        Let AI clients read backup status and manage repositories, plans and
        schedules. Backups and restores keep working without any AI client.
      </Text>
      {error && (
        <Text color="red.500" mb={3}>
          {error}
        </Text>
      )}
      <TabsRoot value={tab} onValueChange={(e: any) => setTab(e.value)}>
        <TabsList mb={4} flexWrap="wrap">
          <TabsTrigger value="approvals">
            Approvals
            {pending > 0 && (
              <Badge ml={1} colorPalette="orange" data-testid="mcp-pending">
                {pending}
              </Badge>
            )}
          </TabsTrigger>
          <TabsTrigger value="connect">Connect</TabsTrigger>
          <TabsTrigger value="tokens">Tokens</TabsTrigger>
          <TabsTrigger value="audit">Audit log</TabsTrigger>
        </TabsList>
        <TabsContent value="approvals">
          <ApprovalsSection
            approvals={approvals}
            status={status}
            reload={reload}
          />
        </TabsContent>
        <TabsContent value="connect">
          <ConnectSection status={status} />
          <ToolsSection status={status} />
        </TabsContent>
        <TabsContent value="tokens">
          <TokensSection status={status} tokens={tokens} reload={reload} />
        </TabsContent>
        <TabsContent value="audit">
          <AuditSection events={events} />
        </TabsContent>
      </TabsRoot>
    </Box>
  );
};

export default McpPage;
