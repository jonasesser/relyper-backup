// relyper: client for the Infrastructure JSON API (/relyper/infra/), which
// sits behind the web UI authentication like the Connect services.
import { backendUrl } from "../../state/buildcfg";

const tokenKey = "backrest-ui-authToken";

export type DataItem = {
  service: string;
  kind:
    | "data"
    | "database"
    | "database_files"
    | "service"
    | "deployment"
    | "compose_directory"
    | "service_folder"
    | "easypanel_settings"
    | "app_definition";
  name: string;
  path?: string;
  state: string;
  plan?: string;
  last_snapshot?: string;
  snapshot_id?: string;
  detail?: string;
  class?: string;
  // SQLite database files inside the data (relative paths).
  sqlite?: string[];
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
  // Easypanel project of an Easypanel service.
  project?: string;
  // Relyper Backup tags; every tag forms a group app (kind "group").
  tags?: string[];
  // Members of a group app.
  members?: string[];
  // Databases the app connects to that run elsewhere.
  database_links?: DatabaseLink[];
  // Containers of earlier Swarm tasks (old deployments): history only.
  old_tasks?: OldTask[];
};

export type DatabaseLink = {
  from: string;
  engine: string;
  host?: string;
  database?: string;
  var: string;
  app?: string;
  service?: string;
};

export type OldTask = {
  service: string;
  container: string;
  image: string;
  state: string;
  status?: string;
};

export type DockerHost = {
  available: boolean;
  endpoint: string;
  version?: string;
  name?: string;
  swarm: boolean;
  easypanel: boolean;
  error?: string;
  // "guard": Relyper Docker guard; "unrestricted": raw socket or a generic proxy.
  access?: "guard" | "unrestricted";
  guard_features?: string[];
  // Plain-language reason when Docker is not reachable.
  hint?: string;
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

export type StorageInfo = {
  free_bytes: number;
  total_bytes: number;
  checked_at: string;
  source: string;
  low: boolean;
};

export type OpSummary = {
  id: number;
  kind: string;
  plan?: string;
  repo: string;
  time: string;
  message?: string;
};

export type Overview = {
  generated_at: string;
  level: string;
  docker_available: boolean;
  apps: Record<string, number>;
  apps_total: number;
  failed_24h: OpSummary[];
  failed_count_24h: number;
  running: OpSummary[];
  last_successful_backup?: OpSummary;
  last_successful_check?: OpSummary;
  last_verified_database_restore?: { subject: string; time: string };
  last_verified_file_restore?: { subject: string; time: string };
  repos: {
    repo: string;
    location: string;
    type: string;
    offsite: boolean;
    storage?: StorageInfo;
  }[];
  attention?: string[];
  backups_24h: Record<string, number>;
};

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
        storage?: StorageInfo;
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
  notes?: string[];
  items?: PlanItem[];
  sources?: {
    service: string;
    source: "git" | "upload" | "image" | "unknown";
    image?: string;
    code: boolean;
    reason: string;
  }[];
  excludes?: string[];
};

export type PlanItem = {
  path: string;
  kind: "config" | "data" | "easypanel" | "dumps" | "inventory";
  service?: string;
  label: string;
  class?: string;
  selected: boolean;
  required?: boolean;
  note?: string;
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

export type SetupOptions = {
  files_cron: string;
  database_cron: string;
  daily: number;
  weekly: number;
  monthly: number;
  yearly: number;
  excludes?: string[];
  limit_upload_kib?: number;
  compression?: string;
};

export type SetupProposal = {
  app: string;
  name: string;
  kind: string;
  plan?: AppPlan;
  error?: string;
  existing_plan: boolean;
  has_databases: boolean;
  size?: { bytes: number; files: number; complete: boolean };
  items?: {
    path: string;
    label: string;
    bytes: number;
    files: number;
    complete: boolean;
  }[];
};

export type SetupProposals = {
  apps: SetupProposal[];
  total_bytes: number;
  complete: boolean;
  storage?: Record<string, StorageInfo>;
  defaults: SetupOptions;
};

export type SetupResult = { app: string; plan_id?: string; error?: string };

export type StageResult = {
  app: string;
  plan: string;
  snapshot_id: string;
  snapshot_time: string;
  target: string;
  operation_ids: number[];
  paths: string[];
};

export type GuideStep = { title: string; detail?: string; commands?: string[] };

export type RecoveryGuide = {
  app: string;
  plan?: string;
  staging: string;
  order: string[];
  steps: GuideStep[];
  notes?: string[];
};

export type PlanUsage = {
  plan: string;
  apps?: string[];
  snapshots: number;
  latest_size_bytes: number;
  latest_added_bytes: number;
  latest_at?: string;
  added_bytes: number;
  without_summary?: number;
};

export type RepoUsage = {
  repo: string;
  location: string;
  stored_bytes: number;
  uncompressed_bytes?: number;
  snapshots: number;
  stats_at?: string;
  storage?: StorageInfo;
  plans: PlanUsage[];
  pinned?: PinnedSnapshot[];
};

export type PinnedSnapshot = {
  id: string;
  plan?: string;
  time: string;
  size_bytes?: number;
};

// PINNED_TAG marks a snapshot kept until it is unpinned (restic tag).
export const PINNED_TAG = "relyper-pinned";

export const infraApi = {
  usage: () => call<RepoUsage[]>("usage"),
  coverage: (refresh = false) =>
    call<CoverageReport>("coverage" + (refresh ? "?refresh=1" : "")),
  readiness: () => call<ReadinessReport>("readiness"),
  inventory: () => call<Inventory>("inventory"),
  overview: () => call<Overview>("overview"),
  results: (app: string, plan: string) =>
    call<Results>(
      `results?app=${encodeURIComponent(app)}&plan=${encodeURIComponent(plan)}`,
    ),
  propose: (app: string) => call<AppPlan>("plan/propose", { app }),
  createPlan: (app: string, repo: string, planId: string, paths?: string[]) =>
    call<AppPlan>("plan/create", { app, repo, plan_id: planId, paths }),
  setPinned: (repo: string, snapshot: string, pinned: boolean) =>
    call<{ repo: string; snapshot: string; pinned: boolean }>("snapshots/pin", {
      repo,
      snapshot,
      pinned,
    }),
  setTags: (app: string, tags: string[]) =>
    call<{ app: string; tags: string[] }>("tags/set", { app, tags }),
  stageRestore: (app: string) => call<StageResult>("restore/stage", { app }),
  recoveryGuide: (app: string, staging?: string) =>
    call<RecoveryGuide>(
      `restore/guide?app=${encodeURIComponent(app)}` +
        (staging ? `&staging=${encodeURIComponent(staging)}` : ""),
    ),
  proposeSetup: (scope: string, apps?: string[]) =>
    call<SetupProposals>("setup/propose", { scope, apps }),
  createSetup: (apps: string[], repo: string, options: SetupOptions) =>
    call<SetupResult[]>("setup/create", { apps, repo, options }),
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
