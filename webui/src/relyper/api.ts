// relyper: Relyper Backup API clients. They share Backrest's transport, so
// they send the same Authorization header.
import { createClient } from "@connectrpc/connect";
import { RelyperSftp } from "../../gen/ts/relyper/v1/sftp_pb";
import { transport } from "../api/client";

export const relyperSftpService = createClient(RelyperSftp, transport);
import { RelyperMcp } from "../../gen/ts/relyper/v1/mcp_pb";

export const relyperMcpService = createClient(RelyperMcp, transport);
