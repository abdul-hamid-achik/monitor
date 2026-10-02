// Types for @thelacanians/monitor (monitor.event.v1 producer).

export type Level = 'fatal' | 'error' | 'warning' | 'info';

export interface Breadcrumb {
  message: string;
  category?: string;
  level?: Level | 'debug';
}

/** One monitor.event.v1 document, as handed to beforeSend. */
export interface MonitorEvent {
  $schema: 'monitor.event.v1';
  event_id: string;
  timestamp: string;
  kind: 'uncaught' | 'rejection' | 'logged' | 'captured' | 'message';
  handled?: boolean;
  level?: Level;
  runtime: 'node' | 'bun' | 'deno';
  error?: SerializedError;
  message?: string;
  tags?: Record<string, string>;
  breadcrumbs?: Array<Breadcrumb & { timestamp: string }>;
  service?: string;
  release?: string;
  environment?: string;
  cwd?: string;
  pid?: number;
  launch_id?: string;
  sdk: { name: string; version: string; mode: 'auto' | 'explicit' };
}

export interface SerializedError {
  type?: string;
  value?: string;
  stack?: string;
  cause?: SerializedError;
}

export interface InitOptions {
  service?: string;
  release?: string;
  environment?: string;
  tags?: Record<string, string>;
  /** Where event files go. Defaults to $MONITOR_EVENTS_DIR, then the global inbox. */
  dir?: string;
  /** Record console calls as breadcrumbs and capture console.error(err). Default true. */
  console?: boolean;
  /** Edit or drop (return null) an event before it is written. */
  beforeSend?: (event: MonitorEvent) => MonitorEvent | null | undefined;
}

export interface CaptureOptions {
  level?: Level;
  tags?: Record<string, string>;
  /** Defaults to true: an explicitly captured error was caught by the app. */
  handled?: boolean;
}

export const version: string;
export function init(options?: InitOptions): typeof sdk;
/** Returns the event id, or undefined when nothing was written. */
export function captureException(error: unknown, options?: CaptureOptions): string | undefined;
export function captureMessage(message: string, level?: Level, options?: Omit<CaptureOptions, 'level' | 'handled'>): string | undefined;
export function captureMessage(message: string, options?: Omit<CaptureOptions, 'handled'>): string | undefined;
export function addBreadcrumb(breadcrumb: Breadcrumb | string): void;
export function setTag(key: string, value: string | number | boolean | null | undefined): void;
export function setTags(tags: Record<string, string | number | boolean | null | undefined>): void;
export function flush(): Promise<boolean>;

declare const sdk: {
  version: typeof version;
  init: typeof init;
  captureException: typeof captureException;
  captureMessage: typeof captureMessage;
  addBreadcrumb: typeof addBreadcrumb;
  setTag: typeof setTag;
  setTags: typeof setTags;
  flush: typeof flush;
};
export default sdk;
