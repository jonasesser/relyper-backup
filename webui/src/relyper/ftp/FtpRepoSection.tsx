// relyper: guided FTP / FTPS setup for the repository form.
//
// Shows a choice between "FTP / FTPS server" and the generic repository URI.
// For FTP it writes the restic URI and the rclone environment variables, so
// nobody has to know about rclone. The server certificate is shown with its
// fingerprint (like FileZilla) and is only trusted after the user confirms
// it; skipping the check is possible but explicit.
import React, { useRef, useState } from "react";
import {
  Badge,
  Box,
  Code,
  Flex,
  Grid,
  Input,
  Stack,
  Text,
  chakra,
} from "@chakra-ui/react";
import { create } from "@bufbuild/protobuf";
import {
  CertificateInfo,
  CertificateStatus,
  FtpTestConnectionRequestSchema,
  FtpTestConnectionResponse,
  FtpTlsMode,
  ObscurePasswordRequestSchema,
  ScanCertificateRequestSchema,
  TrustCertificateRequestSchema,
} from "../../../gen/ts/relyper/v1/ftp_pb";
import {
  Diagnostic,
  RepositoryState,
  StepStatus,
} from "../../../gen/ts/relyper/v1/sftp_pb";
import { Button } from "../../components/ui/button";
import { Field } from "../../components/ui/field";
import { PasswordInput } from "../../components/ui/password-input";
import { Checkbox } from "../../components/ui/checkbox";
import { Radio, RadioGroup } from "../../components/ui/radio";
import { alerts, formatErrorAlert } from "../../components/common/Alerts";
import { relyperFtpService } from "../api";
import {
  FtpSettings,
  FtpTlsModeName,
  buildFtpConfig,
  defaultPort,
  emptyFtpSettings,
  isManagedFtpEnv,
  parseFtpConfig,
} from "./ftpConfig";

export interface FtpRepoSectionProps {
  uri: string | undefined;
  env: string[] | undefined;
  /** Editing an existing repository: the storage type and path are fixed. */
  locked: boolean;
  onChange: (uri: string, env: string[]) => void;
  /** The generic URI field, shown when FTP is not selected. */
  children: React.ReactNode;
}

const modeToProto = (m: FtpTlsModeName) =>
  m === "implicit"
    ? FtpTlsMode.IMPLICIT
    : m === "none"
      ? FtpTlsMode.NONE
      : FtpTlsMode.EXPLICIT;

const stepTitles: Record<string, string> = {
  dns: "Name resolution",
  tcp: "Network connection",
  tls: "Encryption (TLS)",
  login: "Login",
  remote_directory: "Directory",
  write_test: "Write test",
  repository: "Repository",
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

const repositoryText = (s: RepositoryState) => {
  switch (s) {
    case RepositoryState.EXISTING:
      return "An existing restic repository was found.";
    case RepositoryState.EMPTY:
      return "Empty directory: ready for a new repository.";
    case RepositoryState.MISSING:
      return "The directory will be created when the repository is initialized.";
    case RepositoryState.NOT_REPOSITORY:
      return "The directory contains other data. Use an empty subdirectory.";
    default:
      return "";
  }
};

const formatDate = (unix: bigint) =>
  new Date(Number(unix) * 1000).toLocaleDateString();

const DiagnosticBox = ({ d }: { d: Diagnostic }) => (
  <Box
    p={3}
    borderWidth={1}
    borderRadius="md"
    borderColor="red.400"
    bg="red.subtle"
    data-testid="relyper-ftp-diagnostic"
  >
    <Text fontSize="sm" fontWeight="bold" wordBreak="break-word">
      {d.message}
    </Text>
    {d.hint && <Text fontSize="sm">{d.hint}</Text>}
  </Box>
);

const TypeButton = ({
  active,
  disabled,
  onClick,
  title,
  subtitle,
  testId,
}: {
  active: boolean;
  disabled: boolean;
  onClick: () => void;
  title: string;
  subtitle: string;
  testId: string;
}) => (
  <chakra.button
    type="button"
    textAlign="left"
    p={3}
    borderWidth={active ? 2 : 1}
    borderRadius="md"
    borderColor={active ? "blue.500" : "border"}
    bg={active ? "blue.subtle" : undefined}
    opacity={disabled && !active ? 0.5 : 1}
    cursor={disabled ? "default" : "pointer"}
    onClick={disabled ? undefined : onClick}
    aria-pressed={active}
    data-testid={testId}
  >
    <Text fontWeight="semibold" fontSize="sm">
      {title}
    </Text>
    <Text fontSize="xs" color="fg.muted">
      {subtitle}
    </Text>
  </chakra.button>
);

export const FtpRepoSection = ({
  uri,
  env,
  locked,
  onChange,
  children,
}: FtpRepoSectionProps) => {
  const settings = parseFtpConfig(uri, env);
  const active = settings !== null;

  const [password, setPassword] = useState("");
  const [obscuring, setObscuring] = useState(false);
  const obscureSeq = useRef(0);
  const [cert, setCert] = useState<CertificateInfo | null>(null);
  const [certError, setCertError] = useState<Diagnostic | null>(null);
  const [scanning, setScanning] = useState(false);
  const [trusting, setTrusting] = useState(false);
  const [writeTest, setWriteTest] = useState(true);
  const [testing, setTesting] = useState(false);
  const [result, setResult] = useState<FtpTestConnectionResponse | null>(null);

  const apply = (next: FtpSettings) => {
    const b = buildFtpConfig(next, env);
    onChange(b.uri, b.env);
  };

  const update = (patch: Partial<FtpSettings>) => {
    if (!settings) return;
    // Connection details changed: an earlier certificate check or test
    // result no longer applies.
    if (
      "host" in patch ||
      "port" in patch ||
      "mode" in patch ||
      "user" in patch
    ) {
      setResult(null);
    }
    if ("host" in patch || "port" in patch || "mode" in patch) {
      setCert(null);
      setCertError(null);
      // A pinned certificate belongs to host and port.
      patch = { certFile: "", ...patch };
    }
    apply({ ...settings, ...patch });
  };

  const chooseFtp = () => {
    if (active || locked) return;
    const fresh = emptyFtpSettings();
    apply(fresh);
  };

  const chooseOther = () => {
    if (!active || locked || !settings) return;
    onChange(
      "",
      (env || []).filter((e) => !isManagedFtpEnv(e, settings.remote)),
    );
    setPassword("");
    setCert(null);
    setResult(null);
  };

  const onPassword = async (value: string) => {
    setPassword(value);
    setResult(null);
    const seq = ++obscureSeq.current;
    if (!value) {
      if (settings && !locked) update({ obscuredPassword: "" });
      return;
    }
    setObscuring(true);
    try {
      const res = await relyperFtpService.obscurePassword(
        create(ObscurePasswordRequestSchema, { password: value }),
      );
      // Only the latest keystroke wins.
      if (seq === obscureSeq.current && settings) {
        apply({ ...settings, obscuredPassword: res.obscured });
      }
    } catch (e: any) {
      alerts.error(formatErrorAlert(e, "Storing the FTP password failed"));
    } finally {
      if (seq === obscureSeq.current) setObscuring(false);
    }
  };

  const port = settings ? (settings.port ?? defaultPort(settings.mode)) : 21;

  const scan = async () => {
    if (!settings) return;
    setScanning(true);
    setCertError(null);
    try {
      const res = await relyperFtpService.scanCertificate(
        create(ScanCertificateRequestSchema, {
          host: settings.host.trim(),
          port,
          tlsMode: modeToProto(settings.mode),
        }),
      );
      setCert(res.certificate ?? null);
      setCertError(res.error ?? null);
      // Already confirmed earlier (e.g. for another repository): reuse it.
      if (
        res.certificate?.status === CertificateStatus.PINNED &&
        !settings.skipCertificateCheck &&
        settings.certFile !== res.certificate.certFile
      ) {
        apply({ ...settings, certFile: res.certificate.certFile });
      }
    } catch (e: any) {
      alerts.error(formatErrorAlert(e, "Fetching the certificate failed"));
    } finally {
      setScanning(false);
    }
  };

  const trust = async () => {
    if (!settings || !cert) return;
    setTrusting(true);
    try {
      const res = await relyperFtpService.trustCertificate(
        create(TrustCertificateRequestSchema, {
          host: settings.host.trim(),
          port,
          tlsMode: modeToProto(settings.mode),
          fingerprintSha256: cert.fingerprintSha256,
        }),
      );
      apply({
        ...settings,
        certFile: res.certFile,
        skipCertificateCheck: false,
      });
      setCert({
        ...cert,
        status: CertificateStatus.PINNED,
        certFile: res.certFile,
      });
      alerts.success("Certificate confirmed.");
    } catch (e: any) {
      alerts.error(formatErrorAlert(e, "Confirming the certificate failed"));
    } finally {
      setTrusting(false);
    }
  };

  const test = async () => {
    if (!settings) return;
    setTesting(true);
    setResult(null);
    try {
      const res = await relyperFtpService.testConnection(
        create(FtpTestConnectionRequestSchema, {
          host: settings.host.trim(),
          port,
          username: settings.user,
          password,
          obscuredPassword: password ? "" : settings.obscuredPassword,
          remotePath: settings.path.trim(),
          tlsMode: modeToProto(settings.mode),
          certFile: settings.skipCertificateCheck ? "" : settings.certFile,
          skipCertificateCheck: settings.skipCertificateCheck,
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

  const typeChooser = (
    <Field label="Storage type">
      <Grid templateColumns={{ base: "1fr", md: "1fr 1fr" }} gap={2} w="full">
        <TypeButton
          active={active}
          disabled={locked}
          onClick={chooseFtp}
          title="FTP / FTPS server"
          subtitle="e.g. Host Europe backup space"
          testId="relyper-ftp-choose"
        />
        <TypeButton
          active={!active}
          disabled={locked}
          onClick={chooseOther}
          title="Other storage"
          subtitle="Local path, SFTP, S3, B2, … as repository URI"
          testId="relyper-ftp-choose-other"
        />
      </Grid>
    </Field>
  );

  if (!active || !settings) {
    return (
      <>
        {typeChooser}
        {children}
      </>
    );
  }

  const hasStoredPassword = !!settings.obscuredPassword && !password;
  const certStatus = cert?.status;

  return (
    <>
      {typeChooser}
      <Stack gap={4} data-testid="relyper-ftp-section">
        <Grid
          templateColumns={{ base: "1fr", md: "1fr 8rem" }}
          gap={4}
          w="full"
        >
          <Field
            label="Server"
            helperText="Host name only, without ftp:// and without a path."
            required
          >
            <Input
              value={settings.host}
              onChange={(e) => update({ host: e.target.value })}
              placeholder="ftp.example.com"
              autoComplete="off"
              data-testid="relyper-ftp-host"
            />
          </Field>
          <Field label="Port">
            <Input
              type="number"
              min={1}
              max={65535}
              value={settings.port ?? ""}
              placeholder={String(defaultPort(settings.mode))}
              onChange={(e) => {
                const v = parseInt(e.target.value, 10);
                update({ port: Number.isFinite(v) ? v : null });
              }}
              data-testid="relyper-ftp-port"
            />
          </Field>
        </Grid>

        <Field label="Encryption">
          <RadioGroup
            value={settings.mode}
            onValueChange={(e: { value: string | null }) =>
              e.value && update({ mode: e.value as FtpTlsModeName })
            }
          >
            <Stack gap={2}>
              <Radio value="explicit" data-testid="relyper-ftp-mode-explicit">
                Require explicit FTP over TLS (recommended)
              </Radio>
              <Radio value="implicit">Implicit FTP over TLS (port 990)</Radio>
              <Radio value="none">
                Plain FTP (unencrypted, not recommended)
              </Radio>
            </Stack>
          </RadioGroup>
        </Field>
        {settings.mode === "none" && (
          <Text fontSize="sm" color="orange.600">
            Without TLS, the FTP password and the (encrypted) backup data travel
            readable over the network. restic still encrypts the backup content,
            but anyone on the way can capture the FTP login.
          </Text>
        )}

        <Grid templateColumns={{ base: "1fr", md: "1fr 1fr" }} gap={4} w="full">
          <Field label="User" required>
            <Input
              value={settings.user}
              onChange={(e) => update({ user: e.target.value })}
              // Keep the browser from filling in the Relyper login here.
              name="relyper-ftp-user"
              autoComplete="off"
              data-lpignore="true"
              data-1p-ignore="true"
              data-form-type="other"
              data-testid="relyper-ftp-user"
            />
          </Field>
          <Field
            label="FTP password"
            required
            helperText={
              obscuring
                ? "Securing password…"
                : hasStoredPassword
                  ? "A password is stored. Type to replace it."
                  : "Stored obscured, never shown again."
            }
          >
            <PasswordInput
              value={password}
              onChange={(e) => onPassword(e.target.value)}
              placeholder={hasStoredPassword ? "••••••••" : ""}
              name="relyper-ftp-password"
              autoComplete="new-password"
              data-lpignore="true"
              data-1p-ignore="true"
              data-form-type="other"
              data-testid="relyper-ftp-password"
            />
          </Field>
        </Grid>

        <Field
          label="Directory on the server"
          helperText={
            locked
              ? "The directory of an existing repository cannot be changed."
              : "Relative to the FTP login directory. A new, empty directory is best; it is created on initialization."
          }
          required
        >
          <Input
            value={settings.path}
            disabled={locked}
            onChange={(e) => update({ path: e.target.value })}
            placeholder="relyper-backup"
            autoComplete="off"
            data-testid="relyper-ftp-path"
          />
        </Field>

        {settings.mode !== "none" && (
          <Box p={{ base: 3, md: 4 }} borderWidth={1} borderRadius="md">
            <Stack gap={3}>
              <Text fontWeight="bold">Server certificate</Text>
              <Text fontSize="sm">
                Certificates from a public authority are trusted automatically.
                Many hosting providers use certificates that cannot be verified
                that way. Then compare the fingerprint with the one FileZilla or
                your provider shows and confirm it: Relyper Backup then accepts
                exactly this certificate. When the provider renews it, confirm
                the new one here.
              </Text>
              <Box>
                <Button
                  size="sm"
                  onClick={scan}
                  loading={scanning}
                  disabled={!settings.host.trim()}
                  data-testid="relyper-ftp-scan"
                >
                  Check certificate
                </Button>
              </Box>
              {certError && <DiagnosticBox d={certError} />}
              {cert && (
                <Stack gap={1} data-testid="relyper-ftp-cert">
                  <Flex gap={2} align="center" wrap="wrap">
                    {certStatus === CertificateStatus.TRUSTED && (
                      <Badge colorPalette="green">Trusted</Badge>
                    )}
                    {certStatus === CertificateStatus.PINNED && (
                      <Badge colorPalette="green">Confirmed by you</Badge>
                    )}
                    {certStatus === CertificateStatus.UNTRUSTED && (
                      <Badge colorPalette="yellow">Not verified</Badge>
                    )}
                    <Text fontSize="sm" wordBreak="break-word">
                      {cert.subject}
                    </Text>
                  </Flex>
                  <Text fontSize="xs" color="fg.muted" wordBreak="break-word">
                    Issued by {cert.issuer} · valid{" "}
                    {formatDate(cert.notBeforeUnix)} –{" "}
                    {formatDate(cert.notAfterUnix)}
                    {cert.dnsNames.length > 0 &&
                      ` · names: ${cert.dnsNames.join(", ")}`}
                  </Text>
                  <Text fontSize="xs">SHA-256 fingerprint:</Text>
                  <Code fontSize="xs" wordBreak="break-all" whiteSpace="normal">
                    {cert.fingerprintSha256}
                  </Code>
                  {cert.problemMessage &&
                    certStatus !== CertificateStatus.TRUSTED && (
                      <Text fontSize="sm" color="fg.muted">
                        {cert.problemMessage}
                      </Text>
                    )}
                  {certStatus === CertificateStatus.UNTRUSTED &&
                    cert.pinnable && (
                      <Box>
                        <Button
                          size="sm"
                          colorPalette="blue"
                          onClick={trust}
                          loading={trusting}
                          data-testid="relyper-ftp-trust"
                        >
                          Fingerprint matches, trust this certificate
                        </Button>
                      </Box>
                    )}
                  {certStatus === CertificateStatus.UNTRUSTED &&
                    !cert.pinnable && (
                      <Text fontSize="sm" color="orange.600">
                        This certificate cannot be confirmed for {settings.host}
                        . Only "Do not check the certificate" works with this
                        server.
                      </Text>
                    )}
                </Stack>
              )}
              {settings.certFile &&
                !settings.skipCertificateCheck &&
                certStatus !== CertificateStatus.PINNED && (
                  <Text fontSize="sm" color="fg.muted">
                    A confirmed certificate is configured.
                  </Text>
                )}
              <Checkbox
                checked={settings.skipCertificateCheck}
                onCheckedChange={(e) =>
                  update({ skipCertificateCheck: !!e.checked })
                }
                data-testid="relyper-ftp-skip-cert"
              >
                Do not check the certificate
              </Checkbox>
              {settings.skipCertificateCheck && (
                <Text fontSize="sm" color="orange.600">
                  The connection stays encrypted, but any certificate is
                  accepted. Someone who can intercept the connection could pose
                  as the server and capture the FTP login. Confirming the
                  fingerprint is safer.
                </Text>
              )}
            </Stack>
          </Box>
        )}

        <Box p={{ base: 3, md: 4 }} borderWidth={1} borderRadius="md">
          <Stack gap={3}>
            <Text fontWeight="bold">Connection test</Text>
            <Text fontSize="sm">
              Checks name resolution, network, encryption, login, the directory
              and whether a repository already exists there, using the same
              settings backups will use.
            </Text>
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
                disabled={
                  !settings.host.trim() ||
                  !settings.user ||
                  (!password && !settings.obscuredPassword)
                }
                data-testid="relyper-ftp-test"
              >
                Test connection
              </Button>
            </Box>
            {result && (
              <Stack gap={2} data-testid="relyper-ftp-result">
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
                      <Box minW={0}>
                        <Text fontSize="sm" fontWeight="medium">
                          {stepTitles[s.name] ?? s.name}
                        </Text>
                        <Text fontSize="sm" wordBreak="break-word">
                          {s.message}
                        </Text>
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
    </>
  );
};
