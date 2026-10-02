// Types for monitor.app.v1 (docs/contracts/app-protocol-v1.md) and the
// preload bridge (preload/preload.js). Every string that came from a
// monitored process is untrusted: render it as text, never as markup.

export type ConnState = "idle" | "connecting" | "ready" | "error" | "closed";

export interface Hello {
  protocol: string;
  monitor_version: string;
  os: string;
  arch: string;
  hostname?: string;
  read_only: boolean;
  capabilities: string[];
  methods: string[];
  topics: string[];
}

export interface ConnSnapshot {
  id: string;
  name: string;
  kind: "local" | "ssh" | "chalupa";
  host: string | null;
  readOnly: boolean;
  state: ConnState;
  error: string | null;
  hello: Hello | null;
}

export interface ConnectionConfig {
  id: string;
  name: string;
  kind: "local" | "ssh" | "chalupa";
  host?: string;
  env?: string;
  config?: string;
  monitorPath?: string;
  readOnly?: boolean;
}

export interface Settings {
  connections: ConnectionConfig[];
  monitorPath: string;
  editor: "vscode" | "cursor" | "zed" | "idea" | "none";
  notifications: boolean;
  recentLaunches: { cwd: string; command: string; name: string; inspect: boolean; scanStdout: boolean }[];
}

export interface Privacy {
  scrubbed: number;
  text_is_untrusted: boolean;
}

export interface Culprit {
  function?: string;
  fqn?: string;
  file?: string;
  line?: number;
  source?: string;
}

export interface Frame {
  function?: string;
  module?: string;
  filename?: string;
  abs_path?: string;
  lineno?: number;
  colno?: number;
  in_app: boolean;
}

export interface ExceptionInfo {
  type?: string;
  value?: string;
  runtime?: string;
  handled?: boolean;
  frames?: Frame[];
  causes?: { type?: string; culprit?: Culprit }[];
  dropped_frames?: number;
  dropped_causes?: number;
}

export type IssueStatus = "open" | "resolved" | "ignored";

export interface Issue {
  id: string;
  fingerprint: string;
  fingerprint_version: string;
  project: string;
  service: string;
  kind: string;
  title: string;
  message: string;
  exception_type: string;
  symbols: string[];
  severity: string;
  status: IssueStatus;
  first_seen: string;
  last_seen: string;
  occurrence_count: number;
  reopened_count: number;
  resolved_at?: string;
  culprit?: Culprit;
  latest_exception?: ExceptionInfo;
  first_git_sha?: string;
  level?: string;
  handled?: boolean;
  runs?: string[];
  releases?: string[];
}

export interface IssuesList {
  items: Issue[];
  total: number;
  truncated: boolean;
  privacy: Privacy;
}

export interface Snippet {
  start: number;
  lines: string[];
  highlight: number;
  sha256: string;
  stale: boolean;
}

export interface IssueContext {
  schema: "monitor.issue_context.v1";
  budget: string;
  generated_at: string;
  issue: {
    id: string;
    short_id: string;
    status: IssueStatus;
    kind: string;
    title: string;
    exception_type?: string;
    handled?: boolean;
    level?: string;
    project: string;
    service?: string;
  };
  timeline: {
    first_seen: string;
    last_seen: string;
    occurrences: number;
    reopened: number;
    first_git_sha?: string;
    runs?: string[];
    time_source: string;
  };
  culprit?: {
    function?: string;
    fqn?: string;
    file?: string;
    line?: number;
    source: string;
    via?: string;
    confidence: string;
    mapping?: string;
    range?: { start: number; end: number; source: string };
    snippet?: Snippet;
  };
  causes: { type: string; culprit?: { function?: string; file?: string; line?: number } }[];
  frames: { function?: string; file?: string; line?: number; in_app: boolean }[];
  impact: {
    status: string;
    detail?: string;
    recovery?: string;
    callers?: number;
    blast_radius?: number;
    tests?: number;
    test_files?: string[];
    untested?: boolean;
    call_graph?: string;
  };
  last_touched: {
    status: string;
    detail?: string;
    recovery?: string;
    sha?: string;
    subject?: string;
    author_time?: string;
    author_email?: string;
  };
  degraded: { component: string; state: string; detail?: string; recovery?: string }[];
  next: { cli?: string; mcp?: string; why: string }[];
  truncated: { frames?: number; causes?: number };
  privacy: Privacy;
}

export interface IssueGetResult {
  context?: IssueContext;
  root?: string;
  markdown?: string;
  not_found?: boolean;
  recovery?: string;
}

export interface Occurrence {
  id: string;
  issue_id: string;
  observed_at: string;
  project: string;
  service: string;
  title: string;
  message: string;
  run_id?: string;
  release?: string;
  pid?: number;
  count?: number;
  run?: { id?: string; environment?: string; release?: string; git_sha?: string };
}

export interface Histogram {
  start: string;
  bucket_seconds: number;
  buckets: number;
  series: Record<string, number[]>;
  note: string;
}

export interface ProjectSummary {
  project: string;
  services: string[];
  open: number;
  total: number;
  last_seen: string;
  root?: string;
}

export interface IssueDigest {
  id: string;
  short_id: string;
  title: string;
  project: string;
  service?: string;
  kind: string;
  status: IssueStatus;
  exception_type?: string;
  culprit?: Culprit;
  occurrence_count: number;
  reopened_count: number;
  last_seen: string;
  level?: string;
}

export interface IssueEvent {
  type: "new" | "regressed" | "occurrence" | "status";
  issue: IssueDigest;
  delta?: number;
  privacy: Privacy;
}

export interface CompactProcess {
  pid: number;
  parent?: number;
  name: string;
  cpu_percent?: number;
  memory_bytes?: number;
  threads: number;
  status?: string;
  is_system: boolean;
  is_protected: boolean;
}

export interface HostTick {
  snapshot: {
    captured_at: string;
    host: { hostname: string; os: string; platform: string; kernel: string; uptime_seconds: number };
    cpu: { usage_percent?: number; core_count: number; thread_count: number; frequency_mhz?: number; load_avg_1?: number };
    memory: {
      total_bytes?: number;
      used_bytes?: number;
      available_bytes?: number;
      usage_percent?: number;
      swap_total?: number;
      swap_used?: number;
      pressure?: number;
    };
    temperature: { available: boolean; source: string; cpu_package?: number; gpu?: number; fan_rpm?: number };
    network: { bytes_recv_per_sec?: number; bytes_sent_per_sec?: number };
    disk_io: { read_per_sec?: number; write_per_sec?: number };
    filesystems: {
      device: string;
      mount_point: string;
      filesystem: string;
      total_bytes: number;
      used_bytes: number;
      free_bytes: number;
      usage_percent: number;
    }[];
    processes: { system_total: number; matched: number; top_cpu: CompactProcess[]; top_memory: CompactProcess[] };
  };
  per_core_usage: number[];
  load_avg: [number, number, number];
  alerts?: Alert[];
}

export interface Alert {
  severity: string;
  rule: string;
  pid?: number;
  process?: string;
  detail: string;
  diagnosis?: { summary: string; evidence: string[]; confidence: string; next_actions: string[] };
}

export interface ProcessInfo {
  pid: number;
  name: string;
  cpu_percent: number;
  memory: number;
  memory_percent: number;
  threads: number;
  user: string;
  status?: string;
  parent?: number;
  is_system: boolean;
  is_protected: boolean;
}

export interface ProcessList {
  total: number;
  matched: number;
  returned: number;
  truncated: boolean;
  processes: ProcessInfo[];
}

export interface HeatLine {
  line: number;
  self: number;
  cum: number;
  pct_of_function: number;
  code?: string;
  mapping?: string;
  stale?: boolean;
  issues?: { short_id: string; count: number; status: string }[];
}

export interface HeatFunction {
  name: string;
  file: string;
  start_line?: number;
  end_line?: number;
  range_source?: string;
  self_pct: number;
  cum_pct: number;
  lines?: HeatLine[];
  callees?: { func: string; cum: number }[];
}

export interface Heatmap {
  schema: "monitor.line_heatmap.v1";
  profile_type: string;
  unit: string;
  method: string;
  runtime: string;
  samples: number;
  active_samples: number;
  idle_pct: number;
  gc_pct: number;
  functions: HeatFunction[];
  warnings?: string[];
  limitations?: string[];
  inlined_callees?: { caller: string; callee: string; line: number; file?: string }[];
}

export interface ProfileResult {
  heatmap: Heatmap;
  target: { pid: number; name?: string; runtime?: string; codebase_root?: string; main_script?: string };
  method: string;
}

export interface LaunchInfo {
  launch_id: string;
  name: string;
  project: string;
  pid: number;
  started_at: string;
  scan: string;
  alive: boolean;
  inspector_ports?: number[];
}

export interface LogEntry {
  timestamp: string;
  pid: number;
  process: string;
  level: string;
  message: string;
  raw: string;
}

export interface LocalLaunch {
  id: string;
  name: string;
  cwd: string;
  command: string;
  inspect: boolean;
  scanStdout: boolean;
  startedAt: string;
  running: boolean;
  exitCode: number | null;
  signal: string | null;
  lines?: { stream: string; text: string }[];
}

export interface AppInfo {
  version: string;
  electron: string;
  platform: string;
  arch: string;
  packaged: boolean;
  monitor: { path: string; source: string } | null;
}

/** An error from main or from monitor serve, with the protocol's code. */
export interface BridgeError extends Error {
  code: number | null;
  data: unknown;
}

export const Codes = {
  ConfirmRequired: -32001,
  ReadOnly: -32002,
  NotFound: -32003,
  Unavailable: -32004,
  Ambiguous: -32005,
  Refused: -32006,
} as const;

interface Bridge {
  appInfo(): Promise<AppInfo>;
  settings: { get(): Promise<Settings>; update(patch: Partial<Settings>): Promise<Settings> };
  connections: {
    list(): Promise<ConnSnapshot[]>;
    connect(id: string): Promise<void>;
    disconnect(id: string): Promise<void>;
  };
  call<T = unknown>(connId: string, method: string, params?: unknown): Promise<T>;
  launches: {
    start(req: { cwd: string; command: string; name: string; inspect: boolean; scanStdout: boolean }): Promise<LocalLaunch>;
    stop(id: string): Promise<boolean>;
    remove(id: string): Promise<void>;
    list(): Promise<LocalLaunch[]>;
  };
  chalupa: {
    list(): Promise<{
      available: boolean;
      environments: {
        name: string;
        live: boolean;
        model: string;
        tier: string;
        expiresIn: string;
        monitor: { installed: boolean; version: string | null } | null;
      }[];
      error: string | null;
    }>;
  };
  openInEditor(target: { connId: string; root?: string; file?: string; line?: number }): Promise<{ url: string }>;
  copy(text: string): Promise<boolean>;
  openExternal(url: string): Promise<boolean>;
  dialogs: {
    chooseDirectory(defaultPath?: string): Promise<string | null>;
    chooseProfile(): Promise<string | null>;
    chooseBinary(): Promise<string | null>;
    chooseChalupaConfig(): Promise<string | null>;
  };
  on(channel: string, listener: (payload: any) => void): () => void;
  smokeMode: boolean;
  smokeReady(payload: { ok: boolean; reason?: string | null; checks?: Record<string, unknown> }): void;
}

declare global {
  interface Window {
    monitor: Bridge;
  }
}

export const bridge: Bridge = window.monitor;

export function errorCode(error: unknown): number | null {
  const code = (error as BridgeError | undefined)?.code;
  return typeof code === "number" ? code : null;
}

export function errorMessage(error: unknown): string {
  if (error instanceof Error) return error.message;
  return String(error);
}
