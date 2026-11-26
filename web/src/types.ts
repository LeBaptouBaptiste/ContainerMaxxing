// Types des réponses de l'API, alignés sur internal/model/model.go.
// Les slices Go sans `omitempty` peuvent arriver à `null` : on le reflète ici.

export type Status = "success" | "running" | "failed" | "pending" | "unknown" | "skipped";
export type StageID = "image" | "phobos" | "compose" | "github" | "actions" | "ghcr" | "caas";
export type Level = "critical" | "high" | "medium" | "low" | "info";
export type Target = "phobos" | "azure";

export interface Field {
  label: string;
  value: string;
  mono?: boolean;
  link?: string;
}

export interface Step {
  name: string;
  status: Status;
  started_at?: string;
  duration_s: number;
  log?: string;
}

export interface Count {
  label: string;
  value: number;
  level: Level;
}

export interface Finding {
  id: string;
  severity: string;
  target: string;
  title: string;
}

export interface Scan {
  tool: string;
  status: Status;
  summary: string;
  counts: Count[] | null;
  findings?: Finding[];
}

export interface Stage {
  id: StageID;
  label: string;
  status: Status;
  summary: string;
  updated_at: string;
  fields: Field[] | null;
  steps?: Step[];
  scans?: Scan[];
}

export interface ProdInfo {
  tag: string;
  digest: string;
  since: string;
}

export interface RunInfo {
  number: number;
  tag: string;
  status: Status;
  stage?: StageID;
  step_name?: string;
  steps_done: number;
  step_count: number;
  started_at: string;
}

export interface ScenarioInfo {
  id: string;
  label: string;
  description: string;
}

/** Présent uniquement quand les contrôles de démo sont activés côté serveur. */
export interface DemoInfo {
  auth_required: boolean;
  paused: boolean;
  scenario: string;
  scenarios: ScenarioInfo[];
}

export interface Snapshot {
  environment: "local" | "phobos" | "azure";
  source: "mock" | "live";
  app_version: string;
  phobos_url: string;
  azure_url: string;
  prod: ProdInfo;
  run?: RunInfo;
  demo?: DemoInfo;
  stages: Stage[];
  generated_at: string;
}

export interface DeploymentStep {
  name: string;
  status: Status;
  started_at: string;
  duration_s: number;
}

export interface Deployment {
  id: number;
  tag: string;
  digest: string;
  status: Status;
  cve_critical: number;
  cve_high: number;
  cve_medium: number;
  dockle_status: "pass" | "warn" | "fatal" | "skipped";
  signed: boolean;
  target: Target;
  run_url: string;
  started_at: string;
  duration_s: number;
  steps?: DeploymentStep[];
}

export interface Stats {
  total: number;
  succeeded: number;
  failed: number;
  failure_rate: number;
  avg_duration_s: number;
  signed_rate: number;
  by_target: Partial<Record<Target, number>>;
  last_deploy_at?: string;
}
