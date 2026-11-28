// relyper: tests for the FTP / FTPS repository configuration helpers.
import { describe, expect, it } from "vitest";
import {
  buildFtpConfig,
  emptyFtpSettings,
  mergeVisibleEnv,
  parseFtpConfig,
  validateFtpConfig,
  visibleEnv,
} from "./ftpConfig";

describe("ftpConfig", () => {
  it("builds the rclone environment for explicit FTPS with a pinned certificate", () => {
    const { uri, env } = buildFtpConfig(
      {
        ...emptyFtpSettings(),
        host: " wp1.server-he.de ",
        user: "ftpuser",
        obscuredPassword: "OBS",
        path: "relyper",
        certFile: "/data/relyper/ftp-certs/wp1.server-he.de_21.pem",
      },
      ["OTHER=1"],
    );
    expect(uri).toBe("rclone:relyperftp:relyper");
    expect(env).toEqual([
      "OTHER=1",
      "RCLONE_CONFIG_RELYPERFTP_TYPE=ftp",
      "RCLONE_CONFIG_RELYPERFTP_HOST=wp1.server-he.de",
      "RCLONE_CONFIG_RELYPERFTP_PORT=21",
      "RCLONE_CONFIG_RELYPERFTP_USER=ftpuser",
      "RCLONE_CONFIG_RELYPERFTP_PASS=OBS",
      "RCLONE_CONFIG_RELYPERFTP_EXPLICIT_TLS=true",
      "SSL_CERT_FILE=/data/relyper/ftp-certs/wp1.server-he.de_21.pem",
    ]);
  });

  it("round-trips settings, including implicit TLS and a skipped check", () => {
    const s = {
      ...emptyFtpSettings(),
      host: "h",
      user: "u",
      obscuredPassword: "P",
      mode: "implicit" as const,
      port: 2990,
      skipCertificateCheck: true,
      certFile: "/x.pem",
    };
    const { uri, env } = buildFtpConfig(s, []);
    expect(env).toContain("RCLONE_CONFIG_RELYPERFTP_TLS=true");
    expect(env).toContain("RCLONE_CONFIG_RELYPERFTP_NO_CHECK_CERTIFICATE=true");
    expect(env.some((e) => e.startsWith("SSL_CERT_FILE="))).toBe(false);
    expect(parseFtpConfig(uri, env)).toEqual({ ...s, certFile: "" });
  });

  it("recognizes a hand-made remote such as HOSTEUROPE", () => {
    const s = parseFtpConfig("rclone:hosteurope:backups", [
      "RCLONE_CONFIG_HOSTEUROPE_TYPE=ftp",
      "RCLONE_CONFIG_HOSTEUROPE_HOST=wp1.server-he.de",
      "RCLONE_CONFIG_HOSTEUROPE_USER=u",
      "RCLONE_CONFIG_HOSTEUROPE_PASS=P",
      "RCLONE_CONFIG_HOSTEUROPE_EXPLICIT_TLS=true",
    ]);
    expect(s?.remote).toBe("hosteurope");
    expect(s?.mode).toBe("explicit");
    expect(s?.path).toBe("backups");
  });

  it("ignores other repositories", () => {
    expect(parseFtpConfig("sftp:u@h:/p", [])).toBeNull();
    expect(
      parseFtpConfig("rclone:gdrive:x", ["RCLONE_CONFIG_GDRIVE_TYPE=drive"]),
    ).toBeNull();
    expect(visibleEnv("s3:x", ["A=1"])).toEqual(["A=1"]);
  });

  it("hides FTP entries from the generic env list and merges edits back", () => {
    const { uri, env } = buildFtpConfig(
      { ...emptyFtpSettings(), host: "h", user: "u", obscuredPassword: "P" },
      ["A=1"],
    );
    expect(visibleEnv(uri, env)).toEqual(["A=1"]);
    const merged = mergeVisibleEnv(uri, env, ["A=1", "B=2"]);
    expect(merged.filter((e) => e.startsWith("RCLONE_"))).toHaveLength(
      env.length - 1,
    );
    expect(merged).toContain("B=2");
  });

  it("requires host, user and password", () => {
    const { uri, env } = buildFtpConfig(
      { ...emptyFtpSettings(), host: "h", user: "u" },
      [],
    );
    expect(() => validateFtpConfig(uri, env)).toThrow(/password/);
    expect(() => validateFtpConfig("s3:x", [])).not.toThrow();
  });
});
