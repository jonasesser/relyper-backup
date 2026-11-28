// relyper: Relyper Identity (OIDC) sign-in status in Settings. Shows whether
// the sign-in is configured and offered, why not, the callback URL to
// register at the provider and the environment variables to set. Secrets are
// never sent by the server.
import React, { useEffect, useState } from "react";
import { Badge, Box, Code, Flex, Stack, Text } from "@chakra-ui/react";
import { FiKey } from "react-icons/fi";
import { SectionCard } from "../../components/common/SectionCard";
import { backendUrl } from "../../state/buildcfg";

export type IdentitySetupInfo = {
  configured: boolean;
  enabled: boolean;
  error?: string;
  local_auth: boolean;
  issuer?: string;
  client_id?: string;
  client_secret_set: boolean;
  callback_url: string;
  public_url_set: boolean;
  login_role: string;
  admin_role: string;
  operator_tenant?: string;
  scopes: string[];
  display_name: string;
  session_hours: number;
  variables: string[];
  required_claims: string;
};

export const fetchIdentitySetup = async (): Promise<IdentitySetupInfo> => {
  const h: Record<string, string> = {};
  const token = localStorage.getItem("backrest-ui-authToken");
  if (token) h["Authorization"] = "Bearer " + token;
  const resp = await window.fetch(
    new URL(
      "relyper/identity/setup",
      new URL(backendUrl, window.location.href),
    ).toString(),
    { headers: h },
  );
  if (!resp.ok) throw new Error(`${resp.status} ${resp.statusText}`);
  return resp.json();
};

const Row = ({ k, v }: { k: string; v: React.ReactNode }) => (
  <Flex gap={2} wrap="wrap">
    <Text color="fg.muted" minW="120px">
      {k}
    </Text>
    <Box wordBreak="break-all">{v}</Box>
  </Flex>
);

export const IdentitySetup = () => {
  const [s, setS] = useState<IdentitySetupInfo | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    fetchIdentitySetup()
      .then(setS)
      .catch((e) => setError(e?.message || String(e)));
  }, []);

  let state: { label: string; palette: string; hint: string } | null = null;
  if (s) {
    if (!s.configured) {
      state = {
        label: "not configured",
        palette: "gray",
        hint: "Set the environment variables below on the Relyper Backup container and restart it.",
      };
    } else if (s.error) {
      state = {
        label: "configuration error",
        palette: "red",
        hint: s.error,
      };
    } else if (!s.local_auth) {
      state = {
        label: "not offered",
        palette: "orange",
        hint: "Sign-in through Relyper Identity is only offered while authentication is on. Turn it on above and add at least one local user (the fallback admin).",
      };
    } else if (s.enabled) {
      state = {
        label: "active",
        palette: "green",
        hint: `The login page shows "Sign in with ${s.display_name}".`,
      };
    }
  }

  return (
    <SectionCard
      icon={<FiKey size={16} />}
      title="Relyper Identity (OIDC)"
      description="Single sign-on through your identity provider. Configured with environment variables."
    >
      <Stack
        gap={2}
        fontSize="sm"
        wordBreak="break-word"
        css={{ "& code": { wordBreak: "break-all" } }}
        data-testid="identity-setup"
      >
        {error && (
          <Text color="fg.muted">Sign-in status is not available: {error}</Text>
        )}
        {s && state && (
          <>
            <Flex gap={2} align="center" wrap="wrap">
              <Badge colorPalette={state.palette} data-testid="identity-state">
                {state.label}
              </Badge>
              <Text>{state.hint}</Text>
            </Flex>
            {s.issuer && <Row k="Issuer" v={<Code>{s.issuer}</Code>} />}
            {s.client_id && (
              <Row k="Client ID" v={<Code>{s.client_id}</Code>} />
            )}
            {s.configured && (
              <Row
                k="Client secret"
                v={s.client_secret_set ? "set" : "missing"}
              />
            )}
            <Row
              k="Callback URL"
              v={
                <>
                  <Code data-testid="identity-callback">{s.callback_url}</Code>
                  <Text fontSize="xs" color="fg.muted">
                    Register this redirect URI for the client at the provider.
                    {!s.public_url_set &&
                      " It is derived from this request; set RELYPER_PUBLIC_URL to the public address to be sure."}
                  </Text>
                </>
              }
            />
            <Row
              k="Roles"
              v={
                <>
                  login <Code>{s.login_role}</Code>, admin{" "}
                  <Code>{s.admin_role}</Code>
                  {s.operator_tenant && s.operator_tenant !== "*" && (
                    <>
                      , only in tenant <Code>{s.operator_tenant}</Code>
                    </>
                  )}
                </>
              }
            />
            <Row k="Token claims" v={s.required_claims} />
            <Row
              k="Variables"
              v={
                <Flex gap={1} wrap="wrap">
                  {s.variables.map((v) => (
                    <Code key={v}>{v}</Code>
                  ))}
                </Flex>
              }
            />
            <Text fontSize="xs" color="fg.muted">
              Details: IDENTITY.md in the repository.
            </Text>
          </>
        )}
      </Stack>
    </SectionCard>
  );
};

export default IdentitySetup;
