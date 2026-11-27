// relyper: client for the Infrastructure JSON API (/relyper/infra/), which
// sits behind the web UI authentication like the Connect services.
import { backendUrl } from "../../state/buildcfg";

const tokenKey = "backrest-ui-authToken";

export type DataItem = {
  service: string;
  kind: "data" | "database" | "database_files" | "service" | "deployment";
  name: string;
  path?: string;
  state: string;
  plan?: string;
  last_snapshot?: string;
  snapshot_id?: string;
  detail?: string;
  class?: string;
};

export type AppCoverage = {
  app: string;
  name: string;
  kind: string;
  state: string;
  protection: string;
  actions?: string[];
  databases?: string[];
  items: DataItem[];
  plans?: string[];
  notes?: string[];
};

export type DockerHost = {
  available: boolean;
  endpoint: string;
  version?: string;
  name?: string;
  swarm: boolean;
  easypanel: boolean;
  error?: string;
};

export type ServiceInfo = {
  name: string;
  container?: string;
  image: string;
  state: string;
  swarm_service?: string;
  networks?: string[];
  ports?: string[];
  restart_policy?: string;
  memory_limit_bytes?: number;
  cpu_limit?: number;
  depends_on?: string[];
  domains?: string[];
  database?: { engine: string };
  unsupported_database?: string;
};

export type Inventory = {
  apps: { id: string; services: ServiceInfo[] | null }[] | null;
};

export type CoverageReport = {
  generated_at: string;
  docker: DockerHost;
  max_age_hours: number;
  apps: AppCoverage[] | null;
  totals: Record<string, number>;
  plans_without_app?: string[];
};

export type Check = { name: string; status: string; detail: string };

export type ReadinessReport = {
  generated_at: string;
  level: string;
  apps: { app: string; name: string; level: string; checks: Check[] }[] | null;
  repos:
    | {
        repo: string;
        location: string;
        type: string;
        offsite: boolean;
        last_check?: string;
        check_ok: boolean;
      }[]
    | null;
};

export type AppPlan = {
  app: string;
  plan_id: string;
  repo_id?: string;
  paths: string[];
  dump_dir?: string;
  hook_command?: string;
  databases?: string[];
  skipped?: string[];
  warnings?: string[];
};

export type VerifyResult = {
  service: string;
  engine: string;
  ok: boolean;
  problems?: string[];
  expected: Record<string, number>;
  restored: Record<string, number>;
};

export type VerifyRecord = {
  app: string;
  plan: string;
  snapshot_id: string;
  snapshot_time: string;
  time: string;
  ok: boolean;
  error?: string;
  results?: VerifyResult[];
  trigger: string;
};

export type RestoreTestRecord = {
  plan: string;
  snapshot_id: string;
  time: string;
  ok: boolean;
  error?: string;
  files?: { path: string; size: number; ok: boolean; error?: string }[];
  trigger: string;
};

export type Results = {
  database_restore_tests: VerifyRecord[] | null;
  file_restore_tests: RestoreTestRecord[] | null;
};

const url = (path: string) =>
  new URL(
    "relyper/infra/" + path,
    new URL(backendUrl, window.location.href),
  ).toString();

const headers = (): Record<string, string> => {
  const h: Record<string, string> = { "Content-Type": "application/json" };
  const token = localStorage.getItem(tokenKey);
  if (token) h["Authorization"] = "Bearer " + token;
  return h;
};

async function call<T>(path: string, body?: unknown): Promise<T> {
  const resp = await window.fetch(url(path), {
    method: body === undefined ? "GET" : "POST",
    headers: headers(),
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await resp.text();
  let data: any = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    data = null;
  }
  if (!resp.ok) {
    throw new Error(data?.error || `${resp.status} ${resp.statusText}`);
  }
  return data as T;
}

export const infraApi = {
  coverage: (refresh = false) =>
    call<CoverageReport>("coverage" + (refresh ? "?refresh=1" : "")),
  readiness: () => call<ReadinessReport>("readiness"),
  inventory: () => call<Inventory>("inventory"),
  results: (app: string, plan: string) =>
    call<Results>(
      `results?app=${encodeURIComponent(app)}&plan=${encodeURIComponent(plan)}`,
    ),
  propose: (app: string) => call<AppPlan>("plan/propose", { app }),
  createPlan: (app: string, repo: string, planId: string) =>
    call<AppPlan>("plan/create", { app, repo, plan_id: planId }),
  dump: (app: string) => call<{ dumps: unknown[] }>("dump", { app }),
  verify: (app: string) => call<{ message: string }>("verify", { app }),
  restoreTest: (plan: string) =>
    call<{ message: string }>("restore-test", { plan }),
  report: async (): Promise<string> => {
    const resp = await window.fetch(url("report"), { headers: headers() });
    if (!resp.ok) throw new Error(`${resp.status} ${resp.statusText}`);
    return resp.text();
  },
};
