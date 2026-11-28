// relyper: sign-in through Relyper Identity (OIDC) in the login dialog.
//
// The button is a full page navigation to /auth/oidc/login, because the
// identity provider must show its own login page. After the callback the
// server redirects to /?relyper_login=1 and keeps the web UI token for two
// minutes behind an HttpOnly cookie; this component fetches it once and
// stores it like a password login. Local users stay available below as
// emergency access.
import React, { useEffect, useState } from "react";
import { Box, Flex, Separator, Stack, Text } from "@chakra-ui/react";
import { Button } from "../../components/ui/button";
import { setAuthToken } from "../../api/client";
import { backendUrl } from "../../state/buildcfg";

export type IdentityStatus = { enabled: boolean; name?: string };

const authUrl = (path: string) =>
  new URL(path, new URL(backendUrl, window.location.href)).toString();

export const loginErrorMessages: Record<string, string> = {
  forbidden: "Your account has no access to Relyper Backup.",
  missing_email: "Your account has no e-mail address.",
  invalid_state:
    "The sign-in expired or was started in another browser. Try again.",
  provider_unavailable:
    "The sign-in service cannot be reached. Sign in with a local user or try again later.",
  provider_error: "The sign-in was cancelled.",
};

export const fetchIdentityStatus = async (): Promise<IdentityStatus> => {
  try {
    const resp = await window.fetch(authUrl("auth/oidc/status"), {
      cache: "no-store",
    });
    if (!resp.ok) return { enabled: false };
    return (await resp.json()) as IdentityStatus;
  } catch {
    return { enabled: false };
  }
};

// completeHandoff fetches the token after the provider redirected back.
export const completeHandoff = async (): Promise<boolean> => {
  const resp = await window.fetch(authUrl("auth/oidc/session"), {
    method: "POST",
    headers: { "X-Relyper-Login": "1" },
    credentials: "same-origin",
  });
  if (!resp.ok) return false;
  const body = (await resp.json()) as { token?: string; kind?: string };
  if (!body.token) return false;
  if (body.kind === "portal") {
    // A customer signed in on the operator login: their session only works
    // in the customer portal.
    localStorage.setItem("relyper-portal-token", body.token);
    window.location.hash = "#/portal";
    return true;
  }
  setAuthToken(body.token);
  return true;
};

const clearQuery = () => {
  const url = new URL(window.location.href);
  url.searchParams.delete("relyper_login");
  url.searchParams.delete("relyper_login_error");
  window.history.replaceState(null, "", url.toString());
};

export const RelyperLogin = () => {
  const [status, setStatus] = useState<IdentityStatus | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    const code = params.get("relyper_login_error");
    if (code) {
      setError(loginErrorMessages[code] || "The sign-in failed. Try again.");
      clearQuery();
    } else if (params.get("relyper_login") === "1") {
      setBusy(true);
      completeHandoff()
        .then((ok) => {
          clearQuery();
          if (ok) {
            window.location.reload();
          } else {
            setBusy(false);
            setError("The sign-in expired. Try again.");
          }
        })
        .catch(() => {
          clearQuery();
          setBusy(false);
          setError("The sign-in failed. Try again.");
        });
    }
    fetchIdentityStatus().then(setStatus);
  }, []);

  if (!status?.enabled && !error && !busy) return null;

  return (
    <Stack gap={3} mb={4} data-testid="relyper-login">
      {status?.enabled && (
        <Button
          width="full"
          colorPalette="blue"
          loading={busy}
          onClick={() => {
            setBusy(true);
            window.location.assign(authUrl("auth/oidc/login"));
          }}
          data-testid="relyper-login-button"
        >
          Sign in with {status.name || "Relyper"}
        </Button>
      )}
      {error && (
        <Text color="red.500" fontSize="sm" data-testid="relyper-login-error">
          {error}
        </Text>
      )}
      {status?.enabled && (
        <Flex align="center" gap={3}>
          <Separator flex="1" />
          <Box>
            <Text fontSize="xs" color="fg.muted" whiteSpace="nowrap">
              or with a local user
            </Text>
          </Box>
          <Separator flex="1" />
        </Flex>
      )}
    </Stack>
  );
};
