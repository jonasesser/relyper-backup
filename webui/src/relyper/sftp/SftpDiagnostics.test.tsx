import { screen, waitFor } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithProviders } from "../../test/render";
import {
  HostKeyStatus,
  RepositoryState,
  ScanHostKeysResponseSchema,
  StepStatus,
  TestConnectionResponseSchema,
  TrustHostKeyResponseSchema,
} from "../../../gen/ts/relyper/v1/sftp_pb";
import { SftpDiagnostics } from "./SftpDiagnostics";

import { relyperSftpService } from "../api";

const FP = "SHA256:Xtq1Yh0yqf1O0WIRAcDLtQ8QcYGJ5tYF1rN7mXwK2b4";

describe("SftpDiagnostics", () => {
  beforeEach(() => vi.clearAllMocks());

  it("renders nothing for a non-SFTP URI", () => {
    renderWithProviders(
      <SftpDiagnostics
        uri="s3:bucket"
        port={null}
        identityFile=""
        knownHostsPath=""
        onChangeKnownHostsPath={() => {}}
      />,
    );
    expect(screen.queryByTestId("relyper-sftp-diagnostics")).toBeNull();
  });

  it("trusts a host key only after the user confirms it", async () => {
    vi.mocked(relyperSftpService.scanHostKeys)
      .mockResolvedValueOnce(
        create(ScanHostKeysResponseSchema, {
          keys: [
            {
              type: "ssh-ed25519",
              fingerprintSha256: FP,
              status: HostKeyStatus.UNKNOWN,
            },
          ],
        }),
      )
      .mockResolvedValueOnce(
        create(ScanHostKeysResponseSchema, {
          keys: [
            {
              type: "ssh-ed25519",
              fingerprintSha256: FP,
              status: HostKeyStatus.TRUSTED,
            },
          ],
        }),
      );
    vi.mocked(relyperSftpService.trustHostKey).mockResolvedValue(
      create(TrustHostKeyResponseSchema, {
        knownHostsPath: "/config/.backrest-ssh/known_hosts",
      }),
    );
    const onPath = vi.fn();
    const { user } = renderWithProviders(
      <SftpDiagnostics
        uri="sftp://backup@storage.example:2222//restic"
        port={null}
        identityFile="/k"
        knownHostsPath=""
        onChangeKnownHostsPath={onPath}
      />,
    );

    await user.click(screen.getByTestId("relyper-sftp-scan"));
    expect(relyperSftpService.scanHostKeys).toHaveBeenCalledWith(
      expect.objectContaining({ host: "storage.example", port: 2222 }),
    );
    expect(await screen.findByText(FP)).toBeInTheDocument();
    expect(screen.getByText("Not trusted yet")).toBeInTheDocument();
    expect(relyperSftpService.trustHostKey).not.toHaveBeenCalled();

    await user.click(screen.getByTestId("relyper-sftp-trust"));
    await waitFor(() =>
      expect(relyperSftpService.trustHostKey).toHaveBeenCalledWith(
        expect.objectContaining({
          host: "storage.example",
          port: 2222,
          type: "ssh-ed25519",
          fingerprintSha256: FP,
        }),
      ),
    );
    expect(onPath).toHaveBeenCalledWith("/config/.backrest-ssh/known_hosts");
    expect(await screen.findByText("Trusted")).toBeInTheDocument();
    expect(screen.queryByTestId("relyper-sftp-trust")).toBeNull();
  });

  it("offers no trust button for a mismatching key", async () => {
    vi.mocked(relyperSftpService.scanHostKeys).mockResolvedValue(
      create(ScanHostKeysResponseSchema, {
        keys: [
          {
            type: "ssh-ed25519",
            fingerprintSha256: FP,
            status: HostKeyStatus.MISMATCH,
          },
        ],
      }),
    );
    const { user } = renderWithProviders(
      <SftpDiagnostics
        uri="sftp:u@h:/r"
        port={null}
        identityFile=""
        knownHostsPath=""
        onChangeKnownHostsPath={() => {}}
      />,
    );
    await user.click(screen.getByTestId("relyper-sftp-scan"));
    expect(
      await screen.findByText("Different key recorded"),
    ).toBeInTheDocument();
    expect(screen.queryByTestId("relyper-sftp-trust")).toBeNull();
  });

  it("shows the classified failure of a connection test", async () => {
    vi.mocked(relyperSftpService.testConnection).mockResolvedValue(
      create(TestConnectionResponseSchema, {
        ok: false,
        steps: [
          {
            name: "dns",
            status: StepStatus.OK,
            message: "Resolved to 192.0.2.1.",
          },
          { name: "tcp", status: StepStatus.OK, message: "Connected." },
          {
            name: "host_key",
            status: StepStatus.OK,
            message: "Host key is trusted.",
          },
          {
            name: "authentication",
            status: StepStatus.FAILED,
            message: "The server rejected the login.",
            hint: "Add the public key.",
            category: "authentication_failed",
          },
        ],
        error: {
          category: "authentication_failed",
          message: "The server rejected the login.",
          hint: "Add the public key.",
        },
        repositoryState: RepositoryState.UNSPECIFIED,
      }),
    );
    const { user } = renderWithProviders(
      <SftpDiagnostics
        uri="sftp:backup@h:restic"
        port={23}
        identityFile="/config/.backrest-ssh/id"
        knownHostsPath=""
        onChangeKnownHostsPath={() => {}}
      />,
    );
    await user.type(screen.getByTestId("relyper-sftp-password"), "secret");
    await user.click(screen.getByTestId("relyper-sftp-test"));
    await waitFor(() =>
      expect(relyperSftpService.testConnection).toHaveBeenCalledWith(
        expect.objectContaining({
          host: "h",
          port: 23,
          username: "backup",
          remotePath: "restic",
          identityFile: "/config/.backrest-ssh/id",
          password: "secret",
          writeTest: true,
        }),
      ),
    );
    expect(await screen.findByText("Login")).toBeInTheDocument();
    expect(
      screen.getByText("Category: authentication_failed"),
    ).toBeInTheDocument();
  });
});
