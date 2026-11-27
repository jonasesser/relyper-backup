// relyper: tests for the MCP integration page.
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithProviders } from "../../test/render";
import {
  ApprovalListSchema,
  ApprovalSchema,
  AuditEventListSchema,
  CreateTokenResponseSchema,
  McpStatusSchema,
  TokenListSchema,
} from "../../../gen/ts/relyper/v1/mcp_pb";
import { McpPage, endpointUrl } from "./McpPage";
import { relyperMcpService } from "../api";

const pending = create(ApprovalSchema, {
  id: "apr_1",
  tool: "delete_snapshot",
  summary: "Delete snapshot 1234abcd from hosteurope",
  status: "pending",
  tokenName: "Claude",
  paramsJson: '{"repo_id":"hosteurope"}',
  previewJson: "{}",
});

const mockAll = (approvals = [pending]) => {
  vi.mocked(relyperMcpService.getMcpStatus).mockResolvedValue(
    create(McpStatusSchema, {
      endpointPath: "/mcp",
      loginAvailable: true,
      scopes: [
        { name: "status:read", description: "Read status" },
        { name: "data:delete", description: "Delete", restricted: true },
      ],
      tools: [{ name: "get_backup_status", scope: "status:read" }],
    }),
  );
  vi.mocked(relyperMcpService.listTokens).mockResolvedValue(
    create(TokenListSchema, {}),
  );
  vi.mocked(relyperMcpService.listApprovals).mockResolvedValue(
    create(ApprovalListSchema, { approvals }),
  );
  vi.mocked(relyperMcpService.listAuditEvents).mockResolvedValue(
    create(AuditEventListSchema, {}),
  );
};

describe("McpPage", () => {
  beforeEach(() => vi.clearAllMocks());

  it("builds the endpoint URL from the public URL or the origin", () => {
    expect(endpointUrl(null, "https://b.example")).toBe(
      "https://b.example/mcp",
    );
    expect(
      endpointUrl(
        create(McpStatusSchema, {
          publicUrl: "https://backup.example/",
          endpointPath: "/mcp",
        }),
        "http://localhost",
      ),
    ).toBe("https://backup.example/mcp");
  });

  it("shows a pending approval and approves exactly that request", async () => {
    mockAll();
    vi.mocked(relyperMcpService.decideApproval).mockResolvedValue(
      create(ApprovalSchema, { ...pending, status: "approved" }),
    );
    renderWithProviders(<McpPage />);
    expect(
      await screen.findByText("Delete snapshot 1234abcd from hosteurope"),
    ).toBeInTheDocument();
    expect(screen.getByTestId("mcp-pending")).toHaveTextContent("1");
    fireEvent.click(screen.getByTestId("approval-approve"));
    await waitFor(() =>
      expect(relyperMcpService.decideApproval).toHaveBeenCalledTimes(1),
    );
    const req = vi.mocked(relyperMcpService.decideApproval).mock.calls[0][0];
    expect(req.id).toBe("apr_1");
    expect(req.approve).toBe(true);
  });

  it("shows a new token secret once", async () => {
    mockAll([]);
    vi.mocked(relyperMcpService.createToken).mockResolvedValue(
      create(CreateTokenResponseSchema, { secret: "rbk_pat_secret" }),
    );
    renderWithProviders(<McpPage />);
    fireEvent.click(await screen.findByText("Tokens"));
    fireEvent.change(await screen.findByTestId("mcp-token-name"), {
      target: { value: "laptop" },
    });
    fireEvent.click(screen.getByTestId("mcp-token-create"));
    expect(await screen.findByTestId("mcp-token-secret")).toHaveTextContent(
      "rbk_pat_secret",
    );
    const req = vi.mocked(relyperMcpService.createToken).mock.calls[0][0];
    expect(req.name).toBe("laptop");
    expect(req.scopes).toEqual(["status:read"]);
    fireEvent.click(screen.getByText("Done"));
    expect(screen.queryByText("rbk_pat_secret")).toBeNull();
  });
});
