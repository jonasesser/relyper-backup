// relyper: client for the customer portal API (/relyper/portal/api/).
// Portal sessions are kept apart from operator sessions: their own storage
// key, and the server accepts them only on the portal API.
import { backendUrl } from "../../state/buildcfg";

export const portalTokenKey = "relyper-portal-token";

export type RunView = {
  operationId: number;
  status: string;
  startedAt: number;
  endedAt?: number;
  snapshotId?: string;
  bytesAdded?: number;
  bytesTotal?: number;
  filesNew?: number;
  filesChanged?: number;
  errors?: number;
  message?: string;
};

export type PlanView = {
  id: string;
  repo: string;
  paths: string[];
  schedule: string;
  retention?: string;
  state: string;
  ownStorage: boolean;
  lastRun?: RunView;
  lastSuccessAt?: number;
  snapshots: number;
  // Set for plans that back up one Relyper Budget user ("budget").
  app?: string;
  appUser?: string;
};

export type KitView = {
  keyId: string;
  createdAt: string;
  createdBy: string;
  escrowed: boolean;
};

export type RepoView = {
  id: string;
  type: string;
  location: string;
  own: boolean;
  kit?: KitView;
};

export type Problem = { plan: string; state: string; message: string };

export type TenantInfo = { id: string; name: string; escrow: boolean };

export type Overview = {
  tenant: TenantInfo;
  plans: PlanView[];
  repos: RepoView[];
  problems: Problem[];
};

export type Me = {
  tenant: TenantInfo;
  user: { email: string; name: string };
  admin: boolean;
  features: {
    email: boolean;
    storage: boolean;
    kits: boolean;
    restore?: boolean;
  };
};

export type SnapshotView = {
  id: string;
  time: number;
  hostname?: string;
  paths: string[];
  tags?: string[];
  bytes?: number;
  files?: number;
};

export type Notify = { recipients?: string[]; events?: string[] };

export type StorageRequest = {
  name: string;
  type: "s3" | "b2" | "azure" | "rest";
  existing: boolean;
  password?: string;
  endpoint?: string;
  bucket?: string;
  path?: string;
  region?: string;
  keyId?: string;
  secretKey?: string;
};

export class PortalError extends Error {
  status: number;
  constructor(message: string, status: number) {
    super(message);
    this.status = status;
  }
}

const base = () => new URL(backendUrl, window.location.href);

const apiUrl = (path: string) =>
  new URL("relyper/portal/api/" + path, base()).toString();

export const authUrl = (path: string) => new URL(path, base()).toString();

export const getPortalToken = (): string | null => {
  try {
    return localStorage.getItem(portalTokenKey);
  } catch {
    return null;
  }
};

export const setPortalToken = (token: string | null) => {
  try {
    if (token) localStorage.setItem(portalTokenKey, token);
    else localStorage.removeItem(portalTokenKey);
  } catch {
    // Private mode: the session lasts until the tab is closed.
  }
};

async function request(
  method: string,
  path: string,
  body?: unknown,
): Promise<Response> {
  const headers: Record<string, string> = {};
  const token = getPortalToken();
  if (token) headers["Authorization"] = "Bearer " + token;
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const resp = await window.fetch(apiUrl(path), {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
    cache: "no-store",
  });
  if (!resp.ok) {
    let msg = `${resp.status} ${resp.statusText}`;
    try {
      const data = await resp.json();
      if (data?.error) msg = data.error;
    } catch {
      // not JSON
    }
    throw new PortalError(msg, resp.status);
  }
  return resp;
}

async function call<T>(method: string, path: string, body?: unknown) {
  const resp = await request(method, path, body);
  return (await resp.json()) as T;
}

export const portalApi = {
  me: () => call<Me>("GET", "me"),
  overview: () => call<Overview>("GET", "overview"),
  snapshots: (plan: string) =>
    call<SnapshotView[]>("GET", `plans/${encodeURIComponent(plan)}/snapshots`),
  runs: (plan: string) =>
    call<RunView[]>("GET", `plans/${encodeURIComponent(plan)}/runs?limit=20`),
  backup: (plan: string) =>
    call<{ status: string }>(
      "POST",
      `plans/${encodeURIComponent(plan)}/backup`,
      {},
    ),
  // restore adds the budgets of a snapshot as new budgets; nothing is replaced.
  restore: (plan: string, snapshot: string) =>
    call<{ names: string[] }>(
      "POST",
      `plans/${encodeURIComponent(plan)}/restore`,
      { snapshot },
    ),
  notifications: () =>
    call<{ notify: Notify; events: string[]; available: boolean }>(
      "GET",
      "notifications",
    ),
  setNotifications: (n: Notify) =>
    call<{ notify: Notify }>("PUT", "notifications", n),
  testMail: () => call<{ status: string }>("POST", "notifications/test", {}),
  addStorage: (s: StorageRequest) => call<RepoView>("POST", "storage", s),
  removeStorage: (id: string) =>
    call<{ status: string }>("DELETE", `storage/${encodeURIComponent(id)}`, {}),
  copyPlan: (repo: string, plan: string, name: string) =>
    call<{ id: string }>(
      "POST",
      `storage/${encodeURIComponent(repo)}/copy-plan`,
      { plan, name },
    ),
  // createKit returns the kit text; it is shown once and never stored.
  createKit: async (repo: string): Promise<string> => {
    const resp = await request("POST", `kits/${encodeURIComponent(repo)}`, {});
    return resp.text();
  },
};

// completePortalHandoff fetches the session after the provider redirected
// back. Operators who used the portal link get the operator UI instead.
export const completePortalHandoff = async (): Promise<
  "portal" | "operator" | null
> => {
  const resp = await window.fetch(authUrl("auth/oidc/session"), {
    method: "POST",
    headers: { "X-Relyper-Login": "1" },
    credentials: "same-origin",
  });
  if (!resp.ok) return null;
  const body = (await resp.json()) as { token?: string; kind?: string };
  if (!body.token) return null;
  if (body.kind === "portal") {
    setPortalToken(body.token);
    return "portal";
  }
  localStorage.setItem("backrest-ui-authToken", body.token);
  return "operator";
};

export const formatBytes = (n?: number) => {
  if (!n) return "0 B";
  const units = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
};

export const formatTime = (ms?: number) =>
  ms ? new Date(ms).toLocaleString() : "–";
