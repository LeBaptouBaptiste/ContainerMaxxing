// ContainerMaxxing — frontend sans framework, compilé par tsc (aucun bundler).
// app.ts : bandeau, flux temps réel, panneau de détails et historique.
// La chaîne animée est dans chain.ts, les helpers DOM dans dom.ts.

import {
  $, el, svg, safeLink, statusBadge, fmtDur, fmtDate, shortDigest, pct, getJSON, STATUS_LABEL, LEVEL_COLOR,
} from "./dom.js";
import { createChain } from "./chain.js";
import { createDemo } from "./demo.js";
import type { Deployment, DeploymentStep, Field, Scan, Snapshot, Stage, StageID, Stats, Step } from "./types.js";

type SeverityKey = "cve_critical" | "cve_high" | "cve_medium";
const SEVERITIES: ReadonlyArray<{ key: SeverityKey; label: string; color: string }> = [
  { key: "cve_critical", label: "Critical", color: "var(--critical)" },
  { key: "cve_high", label: "High", color: "var(--serious)" },
  { key: "cve_medium", label: "Medium", color: "var(--warning)" },
];

type DrawerTarget = { kind: "stage"; id: StageID } | { kind: "deployment"; id: number };

const state = {
  snapshot: null as Snapshot | null,
  deployments: [] as Deployment[],
  selected: [] as number[],          // ids sélectionnés pour la comparaison (max 2)
  drawer: null as DrawerTarget | null,
  detailCache: new Map<number, Deployment>(),
};

const chain = createChain({ onOpenDetails: (id) => openDrawer({ kind: "stage", id }) });
// Le module démo décide quoi montrer sur la chaîne : le direct, ou un état rejoué.
const demo = createDemo({ display: (snap, frozen) => chain.update(snap, frozen) });

// ---------- bandeau -------------------------------------------------------------

function renderBanner(s: Snapshot): void {
  $("#prod-tag").textContent = s.prod.tag || "—";
  const digest = $("#prod-digest");
  digest.textContent = s.prod.digest ? `@${shortDigest(s.prod.digest)}` : "";
  digest.title = s.prod.digest || "";
  const env = $("#env-badge");
  env.textContent = s.environment;
  env.dataset.env = s.environment;
  $("#source-badge").hidden = s.source !== "mock";
  renderEnvSwitch(s);
  document.title = `${s.prod.tag} · ContainerMaxxing`;
}

// Lien vers l'autre déploiement (phobos ↔ Azure), dans le même onglet.
// Seules les URL https sont acceptées ; en local, pas de lien.
function renderEnvSwitch(s: Snapshot): void {
  const sw = $("#env-switch") as HTMLAnchorElement;
  const target =
    s.environment === "phobos" ? { label: "Voir sur Azure →", url: s.azure_url } :
    s.environment === "azure" ? { label: "Voir sur phobos →", url: s.phobos_url } :
    null;
  let href = "";
  try {
    if (target?.url) {
      const u = new URL(target.url);
      if (u.protocol === "https:") href = u.href;
    }
  } catch { /* URL invalide : pas de lien */ }
  sw.hidden = href === "";
  if (href !== "" && target) {
    sw.href = href;
    sw.textContent = target.label;
    sw.title = href;
  }
}

function setLive(stateName: "open" | "error", text: string): void {
  $("#live").dataset.state = stateName;
  $("#live-text").textContent = text;
}

// ---------- panneau de détails -------------------------------------------------

let lastFocus: HTMLElement | null = null;

function openDrawer(target: DrawerTarget): void {
  lastFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
  state.drawer = target;
  $("#drawer").hidden = false;
  $("#scrim").hidden = false;
  renderDrawer();
  $("#drawer-close").focus();
}

function closeDrawer(): void {
  state.drawer = null;
  $("#drawer").hidden = true;
  $("#scrim").hidden = true;
  lastFocus?.focus();
}

function renderDrawer(): void {
  const d = state.drawer;
  if (!d) return;
  if (d.kind === "stage") {
    const st = state.snapshot?.stages.find((s) => s.id === d.id);
    if (st) renderStageDrawer(st);
  } else {
    void renderDeploymentDrawer(d.id);
  }
}

function section(title: string, ...content: (Node | Node[])[]): HTMLElement {
  return el("section", { class: "drawer-section" }, el("h3", { text: title }), ...content);
}

function fieldsList(fields: Field[]): HTMLElement {
  return el("dl", { class: "fields" }, fields.flatMap((f) => [
    el("dt", { text: f.label }),
    el("dd", { class: f.mono ? "mono" : null }, f.link ? safeLink(f.link, f.value) : f.value),
  ]));
}

function stepsList(steps: (Step | DeploymentStep)[]): HTMLElement {
  return el("ol", { class: "steps" }, steps.map((s) => {
    const row = el("div", { class: "step-row" },
      statusBadge(s.status, s.name),
      el("span", { class: "step-dur", text: s.status === "pending" ? "—" : s.status === "running" ? "…" : fmtDur(s.duration_s) }));
    const log = "log" in s ? s.log : undefined;
    if (!log) return el("li", {}, row);
    return el("li", {}, el("details", {}, el("summary", {}, row), el("pre", { text: log })));
  }));
}

function scanCard(sc: Scan): HTMLElement {
  const card = el("div", { class: "scan" },
    el("div", { class: "scan-head" }, el("span", { class: "scan-tool", text: sc.tool }), statusBadge(sc.status)),
    el("p", { class: "scan-summary", text: sc.summary }));
  if (sc.counts?.length) {
    card.append(el("div", { class: "counts" }, sc.counts.map((c) =>
      el("span", { class: "count" },
        el("span", { class: "swatch", style: { background: LEVEL_COLOR[c.level] ?? "var(--low)" }, "aria-hidden": "true" }),
        `${c.label} `, el("b", { text: String(c.value) })))));
  }
  if (sc.findings?.length) {
    card.append(el("table", { class: "findings" }, el("tbody", {}, sc.findings.map((f) =>
      el("tr", {},
        el("td", { class: "mono", text: f.id }),
        el("td", { text: f.severity }),
        el("td", {}, el("div", { text: f.title }), el("div", { class: "hint mono", text: f.target })))))));
  }
  return card;
}

function renderStageDrawer(st: Stage): void {
  $("#drawer-eyebrow").textContent = `Étape · mise à jour ${new Date(st.updated_at).toLocaleTimeString("fr-FR")}`;
  $("#drawer-title").textContent = st.label;
  const body: HTMLElement[] = [
    el("div", {}, statusBadge(st.status), el("p", { class: "hint", text: st.summary })),
    section("Détails", fieldsList(st.fields ?? [])),
  ];
  if (st.scans?.length) body.push(section("Analyses de sécurité", st.scans.map(scanCard)));
  if (st.steps?.length) body.push(section("Steps", stepsList(st.steps)));
  $("#drawer-body").replaceChildren(...body);
}

async function loadDeployment(id: number): Promise<Deployment> {
  let d = state.detailCache.get(id);
  if (!d) {
    d = await getJSON<Deployment>(`/api/deployments/${encodeURIComponent(id)}`);
    state.detailCache.set(id, d);
  }
  return d;
}

async function renderDeploymentDrawer(id: number): Promise<void> {
  $("#drawer-eyebrow").textContent = "Déploiement";
  $("#drawer-title").textContent = "chargement…";
  $("#drawer-body").replaceChildren();
  let d: Deployment;
  try {
    d = await loadDeployment(id);
  } catch {
    $("#drawer-title").textContent = "Erreur de chargement";
    return;
  }
  if (state.drawer?.kind !== "deployment" || state.drawer.id !== id) return;
  $("#drawer-eyebrow").textContent = `Déploiement #${d.id} · ${d.target}`;
  $("#drawer-title").textContent = d.tag;
  const fields: Field[] = [
    { label: "Statut", value: STATUS_LABEL[d.status] },
    { label: "Cible", value: d.target },
    { label: "Digest", value: d.digest, mono: true },
    { label: "CVE", value: `${d.cve_critical} critical · ${d.cve_high} high · ${d.cve_medium} medium` },
    { label: "Dockle", value: d.dockle_status },
    { label: "Signature", value: d.signed ? "signée (cosign)" : "non signée" },
    { label: "Début", value: new Date(d.started_at).toLocaleString("fr-FR") },
    { label: "Durée", value: fmtDur(d.duration_s) },
  ];
  if (d.run_url) fields.push({ label: "Run", value: "voir sur GitHub", link: d.run_url });
  $("#drawer-body").replaceChildren(
    el("div", {}, statusBadge(d.status)),
    section("Détails", fieldsList(fields)),
    section("Steps", stepsList(d.steps ?? [])),
  );
}

// ---------- historique : stats --------------------------------------------------

function renderStats(s: Stats): void {
  const tile = (label: string, value: string, sub: string) => el("div", { class: "stat" },
    el("span", { class: "eyebrow", text: label }),
    el("div", { class: "stat-value", text: value }),
    el("div", { class: "stat-sub", text: sub }));
  $("#stats").replaceChildren(
    tile("Déploiements", String(s.total), `${s.by_target.azure ?? 0} Azure · ${s.by_target.phobos ?? 0} phobos`),
    tile("Taux d'échec", pct(s.failure_rate), `${s.failed} échec(s) sur ${s.total}`),
    tile("Durée moyenne", fmtDur(s.avg_duration_s), "déploiements réussis"),
    tile("Images signées", pct(s.signed_rate), "cosign · Sigstore"),
  );
}

// ---------- historique : graphique CVE ------------------------------------------

function renderLegend(): void {
  $("#cve-legend").replaceChildren(...SEVERITIES.map((s) =>
    el("span", { class: "legend-item" },
      el("span", { class: "swatch", style: { background: s.color }, "aria-hidden": "true" }), s.label)));
}

function renderChart(items: Deployment[]): void {
  const data = [...items].reverse(); // chronologique
  const W = 1100, H = 220, m = { t: 12, r: 8, b: 40, l: 34 };
  const iw = W - m.l - m.r, ih = H - m.t - m.b;
  const totals = data.map((d) => d.cve_critical + d.cve_high + d.cve_medium);
  const maxV = Math.max(4, ...totals);
  const step = maxV <= 8 ? 2 : maxV <= 20 ? 5 : 10;
  const top = Math.ceil(maxV / step) * step;
  const y = (v: number) => m.t + ih - (v / top) * ih;
  const band = iw / Math.max(data.length, 1);
  const bw = Math.min(28, band * 0.55);

  const root = svg("svg", { viewBox: `0 0 ${W} ${H}`, role: "img", "aria-label": "Nombre de CVE critical, high et medium par déploiement, du plus ancien au plus récent" });
  for (let v = 0; v <= top; v += step) {
    root.append(svg("line", { class: "grid", x1: m.l, x2: W - m.r, y1: y(v), y2: y(v) }));
    root.append(svg("text", { class: "axis-label", x: m.l - 8, y: y(v) + 4, "text-anchor": "end" }, v));
  }
  data.forEach((d, i) => {
    const cx = m.l + band * i + band / 2;
    let base = 0;
    const segs = SEVERITIES.map((s) => ({ ...s, v: d[s.key] })).filter((s) => s.v > 0);
    segs.forEach((s, j) => {
      const y0 = y(base), y1 = y(base + s.v);
      // 2px d'écart entre segments empilés ; extrémité haute arrondie.
      const gap = j > 0 ? 2 : 0;
      const h = Math.max(y0 - y1 - gap, 1);
      root.append(svg("path", { d: barPath(cx - bw / 2, y1, bw, h, j === segs.length - 1 ? 3 : 0), fill: s.color }));
      base += s.v;
    });
    if (d.status === "failed") {
      root.append(svg("text", { class: "fail-mark", x: cx, y: y(base) - 6, "text-anchor": "middle" }, "✕"));
    }
    root.append(svg("text", { class: "tag-label", x: cx, y: H - m.b + 16, "text-anchor": "middle" }, d.tag));
    root.append(svg("text", { class: "axis-label", x: cx, y: H - m.b + 30, "text-anchor": "middle" }, d.target));
    const hit = svg("rect", { class: "hit" + (state.selected.includes(d.id) ? " selected" : ""), x: m.l + band * i, y: m.t, width: band, height: ih + 34 });
    hit.addEventListener("mousemove", (e) => showTooltip(e as MouseEvent, d));
    hit.addEventListener("mouseleave", hideTooltip);
    hit.addEventListener("click", () => openDrawer({ kind: "deployment", id: d.id }));
    root.append(hit);
  });
  $("#cve-chart").replaceChildren(root);
}

function barPath(x: number, yTop: number, w: number, h: number, r: number): string {
  r = Math.min(r, h, w / 2);
  const yb = yTop + h;
  return `M${x},${yb} V${yTop + r} Q${x},${yTop} ${x + r},${yTop} H${x + w - r} Q${x + w},${yTop} ${x + w},${yTop + r} V${yb} Z`;
}

function showTooltip(e: MouseEvent, d: Deployment): void {
  const tt = $("#tooltip");
  const row = (k: string, v: string) => el("div", { class: "tt-row" }, el("span", { text: k }), el("span", { text: v }));
  tt.replaceChildren(
    el("div", { class: "tt-title", text: `${d.tag} · ${d.target}` }),
    row("Statut", STATUS_LABEL[d.status]),
    ...SEVERITIES.map((s) => row(s.label, String(d[s.key]))),
    row("Durée", fmtDur(d.duration_s)),
  );
  tt.hidden = false;
  const x = Math.min(e.clientX + 14, window.innerWidth - tt.offsetWidth - 8);
  tt.style.left = `${x}px`;
  tt.style.top = `${e.clientY + 14}px`;
}
function hideTooltip(): void { $("#tooltip").hidden = true; }

// ---------- historique : timeline ----------------------------------------------

function renderTimeline(items: Deployment[]): void {
  const sevCell = (v: number) => el("td", { class: "num" }, el("span", { class: v ? "sev" : "sev zero", text: String(v) }));
  const rows = items.map((d) => {
    const checked = state.selected.includes(d.id);
    const cb = el("input", {
      type: "checkbox", "aria-label": `Comparer ${d.tag} (${d.target})`, checked,
      onclick: (e: Event) => e.stopPropagation(),
      onchange: () => toggleSelect(d.id),
    });
    return el("tr", { class: checked ? "selected" : null, onclick: () => openDrawer({ kind: "deployment", id: d.id }) },
      el("td", {}, cb),
      el("td", { class: "tag", text: d.tag }),
      el("td", {}, el("span", { class: "pill", dataset: { target: d.target }, text: d.target })),
      el("td", {}, statusBadge(d.status)),
      sevCell(d.cve_critical), sevCell(d.cve_high), sevCell(d.cve_medium),
      el("td", { text: d.dockle_status }),
      el("td", { text: d.signed ? "oui" : "non" }),
      el("td", { class: "num mono", text: fmtDur(d.duration_s) }),
      el("td", { text: fmtDate(d.started_at) }),
      el("td", { onclick: (e: Event) => e.stopPropagation() }, d.run_url ? safeLink(d.run_url, "run ↗") : "—"),
    );
  });
  $("#timeline tbody").replaceChildren(...rows);
}

function toggleSelect(id: number): void {
  const i = state.selected.indexOf(id);
  if (i >= 0) state.selected.splice(i, 1);
  else {
    state.selected.push(id);
    if (state.selected.length > 2) state.selected.shift();
  }
  renderHistory();
}

// ---------- comparaison ----------------------------------------------------------

async function renderCompare(): Promise<void> {
  const box = $("#compare");
  const [idA, idB] = state.selected;
  if (idA === undefined || idB === undefined) { box.hidden = true; return; }
  const [a, b] = await Promise.all([loadDeployment(idA), loadDeployment(idB)]);
  const [older, newer] = new Date(a.started_at) <= new Date(b.started_at) ? [a, b] : [b, a];

  const card = (label: string, va: number, vb: number, fmt: (n: number) => string = String) => {
    const diff = vb - va;
    const trend = diff === 0 ? "same" : diff < 0 ? "better" : "worse";
    const sign = diff > 0 ? "+" : "-";
    return el("div", { class: "delta-card" },
      el("span", { class: "eyebrow", text: label }),
      el("div", { class: "delta-values", text: `${fmt(va)} → ${fmt(vb)}` }),
      el("div", { class: "delta", dataset: { trend }, text: diff === 0 ? "identique" : `${sign}${fmt(Math.abs(diff))}` }));
  };

  const stepNames = [...new Set([...(older.steps ?? []), ...(newer.steps ?? [])].map((s) => s.name))];
  const stepOf = (d: Deployment, n: string) => d.steps?.find((s) => s.name === n);
  const stepCell = (s: DeploymentStep | undefined) =>
    el("td", { class: "num mono", text: s ? (s.status === "success" ? fmtDur(s.duration_s) : STATUS_LABEL[s.status]) : "—" });

  box.replaceChildren(
    el("div", { class: "compare-head" },
      el("h2", { text: `Comparaison ${older.tag} (${older.target}) → ${newer.tag} (${newer.target})` }),
      el("button", { type: "button", class: "btn", onclick: () => { state.selected = []; renderHistory(); } }, "Effacer")),
    el("div", { class: "compare-grid" },
      card("CVE critical", older.cve_critical, newer.cve_critical),
      card("CVE high", older.cve_high, newer.cve_high),
      card("CVE medium", older.cve_medium, newer.cve_medium),
      card("Durée", older.duration_s, newer.duration_s, fmtDur),
      el("div", { class: "delta-card" },
        el("span", { class: "eyebrow", text: "Signature · Dockle" }),
        el("div", { class: "delta-values", text: `${older.signed ? "✓" : "✕"} → ${newer.signed ? "✓" : "✕"}` }),
        el("div", { class: "delta", text: `${older.dockle_status} → ${newer.dockle_status}` }))),
    el("table", { class: "compare-steps" },
      el("thead", {}, el("tr", {}, el("th", { text: "Step" }), el("th", { class: "num", text: older.tag }), el("th", { class: "num", text: newer.tag }))),
      el("tbody", {}, stepNames.map((n) => el("tr", {}, el("td", { text: n }), stepCell(stepOf(older, n)), stepCell(stepOf(newer, n)))))),
  );
  box.hidden = false;
}

// ---------- chargement & temps réel ---------------------------------------------

function renderHistory(): void {
  renderChart(state.deployments);
  renderTimeline(state.deployments);
  renderCompare().catch(console.error);
}

async function refreshHistory(): Promise<void> {
  const [list, stats] = await Promise.all([
    getJSON<{ items: Deployment[] }>("/api/deployments?limit=30"),
    getJSON<Stats>("/api/stats"),
  ]);
  state.deployments = list.items;
  state.selected = state.selected.filter((id) => list.items.some((d) => d.id === id));
  renderStats(stats);
  renderHistory();
}

function onSnapshot(s: Snapshot): void {
  state.snapshot = s;
  renderBanner(s);
  demo.ingest(s);
  if (state.drawer?.kind === "stage") renderDrawer();
}

function connect(): void {
  const es = new EventSource("/api/stream");
  es.addEventListener("open", () => setLive("open", "en direct"));
  es.addEventListener("status", (e) => onSnapshot(JSON.parse((e as MessageEvent<string>).data) as Snapshot));
  es.addEventListener("deployment", () => refreshHistory().catch(console.error));
  // EventSource se reconnecte tout seul ; on se contente d'afficher l'état.
  es.addEventListener("error", () => setLive("error", "reconnexion…"));

  // Mode dev uniquement (le serveur de prod n'émet jamais ces événements) :
  // « reload » quand un fichier du frontend change, « hello » à chaque connexion
  // avec l'identifiant de démarrage, qui change quand le serveur Go est recompilé.
  let bootID: string | null = null;
  es.addEventListener("reload", () => location.reload());
  es.addEventListener("hello", (e) => {
    const { boot } = JSON.parse((e as MessageEvent<string>).data) as { boot: string };
    if (bootID !== null && bootID !== boot) location.reload();
    bootID = boot;
  });
}

function init(): void {
  renderLegend();
  $("#drawer-close").addEventListener("click", closeDrawer);
  $("#scrim").addEventListener("click", closeDrawer);
  document.addEventListener("keydown", (e) => { if (e.key === "Escape" && state.drawer) closeDrawer(); });
  getJSON<Snapshot>("/api/status").then(onSnapshot).catch(() => {});
  refreshHistory().catch(console.error);
  connect();
}

init();
