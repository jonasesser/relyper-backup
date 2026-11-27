// relyper: SFTP host key trust and connection test for the repository form.
//
// Host keys are never trusted automatically: the user compares the SHA-256
// fingerprint with the one the storage provider publishes and confirms it.
import React, { useState } from "react";
import { Badge, Box, Code, Flex, Stack, Text } from "@chakra-ui/react";
import { create } from "@bufbuild/protobuf";
import {
  HostKey,
  HostKeyStatus,
  RepositoryState,
  ScanHostKeysRequestSchema,
  StepStatus,
  TestConnectionRequestSchema,
  TestConnectionResponse,
  TrustHostKeyRequestSchema,
  Diagnostic,
} from "../../../gen/ts/relyper/v1/sftp_pb";
import { Button } from "../../components/ui/button";
import { Field } from "../../components/ui/field";
import { PasswordInput } from "../../components/ui/password-input";
import { Checkbox } from "../../components/ui/checkbox";
import { alerts, formatErrorAlert } from "../../components/common/Alerts";
import { relyperSftpService } from "../api";
import { parseResticSftpUri } from "./sftpUri";

export interface SftpDiagnosticsProps {
  uri: string | undefined;
  port: number | null;
  identityFile: string;
  knownHostsPath: string;
  onChangeKnownHostsPath: (path: string) => void;
}

const statusBadge = (status: HostKeyStatus) => {
  switch (status) {
    case HostKeyStatus.TRUSTED:
      return <Badge colorPalette="green">Trusted</Badge>;
    case HostKeyStatus.MISMATCH:
      return <Badge colorPalette="red">Different key recorded</Badge>;
    case HostKeyStatus.REVOKED:
      return <Badge colorPalette="red">Revoked</Badge>;
    default:
      return <Badge colorPalette="yellow">Not trusted yet</Badge>;
  }
};

const stepIcon = (s: StepStatus) => {
  switch (s) {
    case StepStatus.OK:
      return { icon: "✓", color: "green.500", label: "OK" };
    case StepStatus.WARNING:
      return { icon: "!", color: "orange.500", label: "Warning" };
    case StepStatus.FAILED:
      return { icon: "✗", color: "red.500", label: "Failed" };
    default:
      return { icon: "–", color: "fg.muted", label: "Skipped" };
  }
};

const stepTitles: Record<string, string> = {
  dns: "Name resolution",
  tcp: "Network connection",
  host_key: "Host key",
  authentication: "Login",
  sftp: "SFTP",
  remote_directory: "Remote directory",
  write_test: "Write test",
  repository: "Repository",
  storage: "Free space",
};

const repositoryText = (s: RepositoryState) => {
  switch (s) {
    case RepositoryState.EXISTING:
      return "An existing restic repository was found.";
    case RepositoryState.EMPTY:
      return "Empty directory: ready to initialize a new repository.";
    case RepositoryState.MISSING:
      return "The directory does not exist yet; initializing creates it.";
    case RepositoryState.NOT_REPOSITORY:
      return "The directory contains other data. Use an empty subdirectory.";
    default:
      return "";
  }
};

const DiagnosticBox = ({ d }: { d: Diagnostic }) => (
  <Box
    p={3}
    borderWidth={1}
    borderRadius="md"
    borderColor="red.400"
    bg="red.subtle"
    data-testid="relyper-sftp-diagnostic"
  >
    <Text fontSize="sm" fontWeight="bold">
      {d.message}
    </Text>
    {d.hint && <Text fontSize="sm">{d.hint}</Text>}
    <Text fontSize="xs" color="fg.muted">
      Category: {d.category}
    </Text>
  </Box>
);

export const SftpDiagnostics = ({
  uri,
  port,
  identityFile,
  knownHostsPath,
  onChangeKnownHostsPath,
}: SftpDiagnosticsProps) => {
  const parsed = uri ? parseResticSftpUri(uri) : null;
  const effectivePort = port ?? parsed?.port ?? 22;

  const [keys, setKeys] = useState<HostKey[] | null>(null);
  const [scanError, setScanError] = useState<Diagnostic | null>(null);
  const [scanning, setScanning] = useState(false);
  const [trusting, setTrusting] = useState<string | null>(null);

  const [password, setPassword] = useState("");
  const [writeTest, setWriteTest] = useState(true);
  const [testing, setTesting] = useState(false);
  const [result, setResult] = useState<TestConnectionResponse | null>(null);

  if (!parsed) return null;

  const scan = async () => {
    setScanning(true);
    setScanError(null);
    try {
      const res = await relyperSftpService.scanHostKeys(
        create(ScanHostKeysRequestSchema, {
          host: parsed.host,
          port: effectivePort,
        }),
      );
      setKeys(res.keys);
      setScanError(res.error ?? null);
    } catch (e: any) {
      alerts.error(formatErrorAlert(e, "Fetching the host key failed"));
    } finally {
      setScanning(false);
    }
  };

  const trust = async (k: HostKey) => {
    setTrusting(k.fingerprintSha256);
    try {
      const res = await relyperSftpService.trustHostKey(
        create(TrustHostKeyRequestSchema, {
          host: parsed.host,
          port: effectivePort,
          type: k.type,
          fingerprintSha256: k.fingerprintSha256,
        }),
      );
      onChangeKnownHostsPath(res.knownHostsPath);
      alerts.success(`Host key ${k.fingerprintSha256} is now trusted.`);
      await scan();
    } catch (e: any) {
      alerts.error(formatErrorAlert(e, "Trusting the host key failed"));
    } finally {
      setTrusting(null);
    }
  };

  const test = async () => {
    setTesting(true);
    setResult(null);
    try {
      const res = await relyperSftpService.testConnection(
        create(TestConnectionRequestSchema, {
          host: parsed.host,
          port: effectivePort,
          username: parsed.user ?? "",
          identityFile,
          password,
          remotePath: parsed.path,
          writeTest,
        }),
      );
      setResult(res);
    } catch (e: any) {
      alerts.error(formatErrorAlert(e, "Connection test failed"));
    } finally {
      setTesting(false);
    }
  };

  const hasUntrusted = keys?.some((k) => k.status === HostKeyStatus.UNKNOWN);
  const hasMismatch = keys?.some(
    (k) =>
      k.status === HostKeyStatus.MISMATCH || k.status === HostKeyStatus.REVOKED,
  );

  return (
    <Stack gap={4} data-testid="relyper-sftp-diagnostics">
      <Box p={4} borderWidth={1} borderRadius="md">
        <Stack gap={3}>
          <Text fontWeight="bold">
            Host key of {parsed.host}:{effectivePort}
          </Text>
          <Text fontSize="sm">
            Relyper Backup only connects to servers whose host key you have
            confirmed. Compare the fingerprint below with the one your storage
            provider publishes (control panel, support page or{" "}
            <Code>ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub</Code> on the
            server). Trust a key only if it matches.
          </Text>
          <Box>
            <Button
              size="sm"
              onClick={scan}
              loading={scanning}
              data-testid="relyper-sftp-scan"
            >
              Fetch host key
            </Button>
          </Box>
          {scanError && <DiagnosticBox d={scanError} />}
          {keys?.map((k) => (
            <Flex
              key={k.fingerprintSha256}
              gap={3}
              align="center"
              wrap="wrap"
              data-testid="relyper-sftp-hostkey"
            >
              <Code>{k.type}</Code>
              <Code wordBreak="break-all">{k.fingerprintSha256}</Code>
              {statusBadge(k.status)}
              {k.status === HostKeyStatus.UNKNOWN && (
                <Button
                  size="xs"
                  colorPalette="blue"
                  loading={trusting === k.fingerprintSha256}
                  onClick={() => trust(k)}
                  data-testid="relyper-sftp-trust"
                >
                  Fingerprint matches, trust this key
                </Button>
              )}
            </Flex>
          ))}
          {hasMismatch && (
            <Text fontSize="sm" color="red.600">
              A different key is recorded for this server. This happens when the
              server was reinstalled, but it can also mean that someone is
              intercepting the connection. Relyper Backup will not replace the
              recorded key. Confirm the new fingerprint with your provider and
              then edit <Code>{knownHostsPath || "known_hosts"}</Code> by hand.
            </Text>
          )}
          {hasUntrusted && !hasMismatch && (
            <Text fontSize="sm" color="fg.muted">
              Trusting one key type is enough.
            </Text>
          )}
        </Stack>
      </Box>

      <Box p={4} borderWidth={1} borderRadius="md">
        <Stack gap={3}>
          <Text fontWeight="bold">Connection test</Text>
          <Text fontSize="sm">
            Checks name resolution, network, host key, login, SFTP, the remote
            directory and whether a repository already exists there.
          </Text>
          <Field
            label="SSH password for this test (optional)"
            helperText="Only used for this test and never stored. Scheduled backups need key authentication."
          >
            <PasswordInput
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoComplete="off"
              data-testid="relyper-sftp-password"
            />
          </Field>
          <Checkbox
            checked={writeTest}
            onCheckedChange={(e) => setWriteTest(!!e.checked)}
          >
            Write test (creates and deletes one small file)
          </Checkbox>
          <Box>
            <Button
              size="sm"
              onClick={test}
              loading={testing}
              data-testid="relyper-sftp-test"
            >
              Test connection
            </Button>
          </Box>
          {result && (
            <Stack gap={2} data-testid="relyper-sftp-result">
              {result.steps.map((s) => {
                const ic = stepIcon(s.status);
                return (
                  <Flex key={s.name} gap={2} align="flex-start">
                    <Text
                      color={ic.color}
                      fontWeight="bold"
                      minW="1.2em"
                      aria-label={ic.label}
                    >
                      {ic.icon}
                    </Text>
                    <Box>
                      <Text fontSize="sm" fontWeight="medium">
                        {stepTitles[s.name] ?? s.name}
                      </Text>
                      <Text fontSize="sm">{s.message}</Text>
                      {s.hint && (
                        <Text fontSize="sm" color="fg.muted">
                          {s.hint}
                        </Text>
                      )}
                    </Box>
                  </Flex>
                );
              })}
              {result.ok ? (
                <Text fontSize="sm" color="green.600" fontWeight="bold">
                  Connection works. {repositoryText(result.repositoryState)}
                </Text>
              ) : (
                result.error && <DiagnosticBox d={result.error} />
              )}
            </Stack>
          )}
        </Stack>
      </Box>
    </Stack>
  );
};
