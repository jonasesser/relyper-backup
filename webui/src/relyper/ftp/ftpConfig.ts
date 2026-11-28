// relyper: FTP / FTPS repository configuration.
//
// restic reaches FTP servers through rclone. An FTP repository is stored as a
// normal Backrest repository: the URI "rclone:<remote>:<path>" plus the
// rclone remote in the repository's environment (RCLONE_CONFIG_<REMOTE>_*).
// The password is kept in rclone's obscured form; a confirmed FTPS
// certificate is pinned through SSL_CERT_FILE.

export type FtpTlsModeName = "explicit" | "implicit" | "none";

export interface FtpSettings {
  remote: string; // rclone remote name, e.g. "relyperftp"
  host: string;
  port: number | null; // null: default of the mode
  user: string;
  obscuredPassword: string;
  path: string;
  mode: FtpTlsModeName;
  skipCertificateCheck: boolean;
  certFile: string;
}

export const DEFAULT_FTP_REMOTE = "relyperftp";

export const defaultPort = (mode: FtpTlsModeName) =>
  mode === "implicit" ? 990 : 21;

export const emptyFtpSettings = (): FtpSettings => ({
  remote: DEFAULT_FTP_REMOTE,
  host: "",
  port: null,
  user: "",
  obscuredPassword: "",
  path: "relyper-backup",
  mode: "explicit",
  skipCertificateCheck: false,
  certFile: "",
});

const URI_RE = /^rclone:([A-Za-z0-9_]+):(.*)$/;

const prefixOf = (remote: string) => `RCLONE_CONFIG_${remote.toUpperCase()}_`;

const envValue = (env: string[], key: string): string | undefined => {
  const hit = env.find((e) => e.startsWith(key + "="));
  return hit === undefined ? undefined : hit.substring(key.length + 1);
};

const isTrue = (v: string | undefined) =>
  v !== undefined && ["true", "1", "yes"].includes(v.trim().toLowerCase());

/** Returns the FTP settings when uri and env describe an FTP repository. */
export const parseFtpConfig = (
  uri: string | undefined,
  env: string[] | undefined,
): FtpSettings | null => {
  const m = uri ? URI_RE.exec(uri) : null;
  if (!m) return null;
  const list = env || [];
  const p = prefixOf(m[1]);
  if (
    envValue(list, p + "TYPE")
      ?.trim()
      .toLowerCase() !== "ftp"
  )
    return null;
  const port = parseInt(envValue(list, p + "PORT") ?? "", 10);
  const mode: FtpTlsModeName = isTrue(envValue(list, p + "TLS"))
    ? "implicit"
    : isTrue(envValue(list, p + "EXPLICIT_TLS"))
      ? "explicit"
      : "none";
  return {
    remote: m[1],
    host: envValue(list, p + "HOST") ?? "",
    port: Number.isFinite(port) && port !== defaultPort(mode) ? port : null,
    user: envValue(list, p + "USER") ?? "",
    obscuredPassword: envValue(list, p + "PASS") ?? "",
    path: m[2],
    mode,
    skipCertificateCheck: isTrue(envValue(list, p + "NO_CHECK_CERTIFICATE")),
    certFile: envValue(list, "SSL_CERT_FILE") ?? "",
  };
};

/** Whether an env entry is written by the FTP form for this remote. */
export const isManagedFtpEnv = (entry: string, remote: string) =>
  entry.startsWith(prefixOf(remote)) || entry.startsWith("SSL_CERT_FILE=");

/** Builds uri and env; env entries the form does not manage are kept. */
export const buildFtpConfig = (
  s: FtpSettings,
  currentEnv: string[] | undefined,
): { uri: string; env: string[] } => {
  const p = prefixOf(s.remote);
  const keep = (currentEnv || []).filter((e) => !isManagedFtpEnv(e, s.remote));
  const env = [
    `${p}TYPE=ftp`,
    `${p}HOST=${s.host.trim()}`,
    `${p}PORT=${s.port ?? defaultPort(s.mode)}`,
    `${p}USER=${s.user}`,
  ];
  if (s.obscuredPassword) env.push(`${p}PASS=${s.obscuredPassword}`);
  if (s.mode === "explicit") env.push(`${p}EXPLICIT_TLS=true`);
  if (s.mode === "implicit") env.push(`${p}TLS=true`);
  if (s.mode !== "none") {
    if (s.skipCertificateCheck) env.push(`${p}NO_CHECK_CERTIFICATE=true`);
    else if (s.certFile) env.push(`SSL_CERT_FILE=${s.certFile}`);
  }
  return { uri: `rclone:${s.remote}:${s.path.trim()}`, env: [...keep, ...env] };
};

/** Env entries to show in the generic list (FTP entries are in the form). */
export const visibleEnv = (uri: string | undefined, env: string[]) => {
  const s = parseFtpConfig(uri, env);
  return s ? env.filter((e) => !isManagedFtpEnv(e, s.remote)) : env;
};

/** Merges edits of the generic env list back with the FTP entries. */
export const mergeVisibleEnv = (
  uri: string | undefined,
  env: string[],
  edited: string[],
) => {
  const s = parseFtpConfig(uri, env);
  return s
    ? [...edited, ...env.filter((e) => isManagedFtpEnv(e, s.remote))]
    : edited;
};

/** Throws a readable error when an FTP repository is incomplete. */
export const validateFtpConfig = (
  uri: string | undefined,
  env: string[] | undefined,
) => {
  const s = parseFtpConfig(uri, env);
  if (!s) return;
  if (!s.host.trim()) throw new Error("FTP server is missing.");
  if (!s.user.trim()) throw new Error("FTP user name is missing.");
  if (!s.obscuredPassword)
    throw new Error("FTP password is missing (or still being processed).");
};
