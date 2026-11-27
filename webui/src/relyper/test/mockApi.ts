// relyper: stand-in for src/relyper/api.ts, installed globally by
// src/test/setup.tsx. It must mirror the real module's exports.
import { vi } from "vitest";

export const relyperSftpService = {
  scanHostKeys: vi.fn(),
  trustHostKey: vi.fn(),
  testConnection: vi.fn(),
};

export const relyperMcpService = {
  getMcpStatus: vi.fn(),
  listTokens: vi.fn(),
  createToken: vi.fn(),
  revokeToken: vi.fn(),
  listApprovals: vi.fn(),
  decideApproval: vi.fn(),
  listAuditEvents: vi.fn(),
};
