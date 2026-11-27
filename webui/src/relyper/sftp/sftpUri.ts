// relyper: parses restic SFTP repository URIs including the remote path.
//
// restic accepts two shapes (see restic docs, "SFTP"):
//   sftp:[user@]host:path          scp-style, no port; "/abs" or "rel"
//   sftp://[user@]host[:port]/path URL-style; "//abs" is absolute,
//                                  "/rel" is relative to the home directory
export interface ParsedSftpUri {
  user?: string;
  host: string;
  port?: number;
  // As restic interprets it: starts with "/" when absolute, otherwise it is
  // relative to the login directory. Empty means the login directory.
  path: string;
}

const splitUser = (authority: string): { user?: string; hostPart: string } => {
  const at = authority.lastIndexOf("@");
  if (at === -1) return { hostPart: authority };
  return { user: authority.slice(0, at), hostPart: authority.slice(at + 1) };
};

const stripBrackets = (host: string) =>
  host.startsWith("[") && host.endsWith("]") ? host.slice(1, -1) : host;

export const parseResticSftpUri = (uri: string): ParsedSftpUri | null => {
  if (!uri.startsWith("sftp:")) return null;
  let rest = uri.slice("sftp:".length);

  if (rest.startsWith("//")) {
    rest = rest.slice(2);
    const slash = rest.indexOf("/");
    const authority = slash === -1 ? rest : rest.slice(0, slash);
    const rawPath = slash === -1 ? "" : rest.slice(slash);
    const { user, hostPart } = splitUser(authority);
    let host = hostPart;
    let port: number | undefined;
    const m = hostPart.match(/^(\[[^\]]+\]|[^:]+):(\d+)$/);
    if (m) {
      host = m[1];
      port = parseInt(m[2], 10);
    }
    // "//abs" -> "/abs"; "/rel" -> "rel"
    const path = rawPath.startsWith("//")
      ? rawPath.slice(1)
      : rawPath.replace(/^\//, "");
    host = stripBrackets(host);
    if (!host) return null;
    return { user, host, port, path };
  }

  // scp-style: the first ':' after the authority delimits the path.
  let authority: string;
  let path: string;
  if (rest.includes("[")) {
    const close = rest.indexOf("]");
    authority = rest.slice(0, close + 1);
    path = rest.slice(close + 2);
  } else {
    const colon = rest.indexOf(":");
    authority = colon === -1 ? rest : rest.slice(0, colon);
    path = colon === -1 ? "" : rest.slice(colon + 1);
  }
  const { user, hostPart } = splitUser(authority);
  const host = stripBrackets(hostPart);
  if (!host) return null;
  return { user, host, path };
};
