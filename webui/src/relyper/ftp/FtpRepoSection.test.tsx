// relyper: tests for the guided FTP / FTPS form.
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";
import React, { useState } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithProviders } from "../../test/render";
import {
  CertificateStatus,
  FtpTestConnectionResponseSchema,
  ObscurePasswordResponseSchema,
  ScanCertificateResponseSchema,
  TrustCertificateResponseSchema,
} from "../../../gen/ts/relyper/v1/ftp_pb";
import {
  RepositoryState,
  StepStatus,
} from "../../../gen/ts/relyper/v1/sftp_pb";
import { FtpRepoSection } from "./FtpRepoSection";
import { parseFtpConfig } from "./ftpConfig";
import { relyperFtpService } from "../api";

const FP =
  "C8:53:ED:CA:4C:DE:08:13:05:FA:77:C0:F2:18:A2:98:E9:84:B0:35:E9:C3:2A:AD:B2:D8:B8:A8:9B:7B:2A:14";

let latest: { uri: string; env: string[] } = { uri: "", env: [] };

const Harness = ({ locked = false }: { locked?: boolean }) => {
  const [state, setState] = useState<{ uri: string; env: string[] }>({
    uri: "",
    env: [],
  });
  latest = state;
  return (
    <FtpRepoSection
      uri={state.uri}
      env={state.env}
      locked={locked}
      onChange={(uri, env) => setState({ uri, env })}
    >
      <div data-testid="uri-field" />
    </FtpRepoSection>
  );
};

const type = (testId: string, value: string) =>
  fireEvent.change(screen.getByTestId(testId), { target: { value } });

describe("FtpRepoSection", () => {
  beforeEach(() => vi.clearAllMocks());

  it("shows the URI field until FTP is chosen", () => {
    renderWithProviders(<Harness />);
    expect(screen.getByTestId("uri-field")).toBeTruthy();
    fireEvent.click(screen.getByTestId("relyper-ftp-choose"));
    expect(screen.queryByTestId("uri-field")).toBeNull();
    expect(screen.getByTestId("relyper-ftp-section")).toBeTruthy();
    expect(latest.uri).toBe("rclone:relyperftp:relyper-backup");
  });

  it("writes an obscured password, never the plain one", async () => {
    vi.mocked(relyperFtpService.obscurePassword).mockResolvedValue(
      create(ObscurePasswordResponseSchema, { obscured: "OBSCURED" }),
    );
    renderWithProviders(<Harness />);
    fireEvent.click(screen.getByTestId("relyper-ftp-choose"));
    type("relyper-ftp-host", "wp1.server-he.de");
    type("relyper-ftp-user", "ftpuser");
    type("relyper-ftp-password", "plain secret");
    await waitFor(() =>
      expect(parseFtpConfig(latest.uri, latest.env)?.obscuredPassword).toBe(
        "OBSCURED",
      ),
    );
    expect(latest.env.join("\n")).not.toContain("plain secret");
    expect(latest.uri).not.toContain("plain secret");
    expect(latest.env).toContain("RCLONE_CONFIG_RELYPERFTP_EXPLICIT_TLS=true");
  });

  it("pins a certificate only after the user confirms the fingerprint", async () => {
    vi.mocked(relyperFtpService.scanCertificate).mockResolvedValue(
      create(ScanCertificateResponseSchema, {
        certificate: {
          subject: "CN=wp1.server-he.de",
          issuer: "CN=wp1.server-he.de",
          fingerprintSha256: FP,
          status: CertificateStatus.UNTRUSTED,
          problem: "unknown_authority",
          problemMessage: "self-signed",
          pinnable: true,
        },
      }),
    );
    vi.mocked(relyperFtpService.trustCertificate).mockResolvedValue(
      create(TrustCertificateResponseSchema, { certFile: "/data/pin.pem" }),
    );
    renderWithProviders(<Harness />);
    fireEvent.click(screen.getByTestId("relyper-ftp-choose"));
    type("relyper-ftp-host", "wp1.server-he.de");
    fireEvent.click(screen.getByTestId("relyper-ftp-scan"));
    await screen.findByText(FP);
    expect(relyperFtpService.trustCertificate).not.toHaveBeenCalled();
    expect(latest.env.some((e) => e.startsWith("SSL_CERT_FILE="))).toBe(false);
    fireEvent.click(screen.getByTestId("relyper-ftp-trust"));
    await waitFor(() =>
      expect(latest.env).toContain("SSL_CERT_FILE=/data/pin.pem"),
    );
    expect(
      vi.mocked(relyperFtpService.trustCertificate).mock.calls[0][0],
    ).toMatchObject({
      host: "wp1.server-he.de",
      port: 21,
      fingerprintSha256: FP,
    });
  });

  it("can skip the certificate check explicitly", async () => {
    renderWithProviders(<Harness />);
    fireEvent.click(screen.getByTestId("relyper-ftp-choose"));
    const box = screen
      .getByTestId("relyper-ftp-skip-cert")
      .querySelector('input[type="checkbox"]') as HTMLInputElement;
    fireEvent.click(box);
    await waitFor(() =>
      expect(latest.env).toContain(
        "RCLONE_CONFIG_RELYPERFTP_NO_CHECK_CERTIFICATE=true",
      ),
    );
    expect(screen.getByText(/any certificate is\s+accepted/)).toBeTruthy();
  });

  it("runs the connection test and shows the steps", async () => {
    vi.mocked(relyperFtpService.obscurePassword).mockResolvedValue(
      create(ObscurePasswordResponseSchema, { obscured: "OBS" }),
    );
    vi.mocked(relyperFtpService.testConnection).mockResolvedValue(
      create(FtpTestConnectionResponseSchema, {
        ok: true,
        repositoryState: RepositoryState.MISSING,
        steps: [
          { name: "login", status: StepStatus.OK, message: "Logged in as u." },
        ],
      }),
    );
    renderWithProviders(<Harness />);
    fireEvent.click(screen.getByTestId("relyper-ftp-choose"));
    type("relyper-ftp-host", "h");
    type("relyper-ftp-user", "u");
    type("relyper-ftp-password", "pw");
    await waitFor(() =>
      expect(
        (screen.getByTestId("relyper-ftp-test") as HTMLButtonElement).disabled,
      ).toBe(false),
    );
    fireEvent.click(screen.getByTestId("relyper-ftp-test"));
    await screen.findByText("Logged in as u.");
    expect(screen.getByText(/Connection works/)).toBeTruthy();
    expect(
      vi.mocked(relyperFtpService.testConnection).mock.calls[0][0],
    ).toMatchObject({
      host: "h",
      username: "u",
      password: "pw",
      writeTest: true,
    });
  });

  it("does not offer to switch the type of an existing repository", () => {
    renderWithProviders(<Harness locked />);
    fireEvent.click(screen.getByTestId("relyper-ftp-choose"));
    expect(screen.getByTestId("uri-field")).toBeTruthy();
  });
});
