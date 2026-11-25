// ContainerMaxxing — frontend sans dépendance ni build.
// Règle de sécurité : aucune donnée n'est insérée via innerHTML ; tout passe
// par textContent / createElement, et les liens ne sont acceptés qu'en https.

const STATUS_LABEL = {
  success: "OK", running: "en cours", failed: "échec", pending: "en attente", unknown: "inconnu",
};
const STATUS_ICON = { success: "✓", failed: "✕", running: "", pending: "", unknown: "?" };
const SEVERITIES = [
  { key: "cve_critical", label: "Critical", color: "var(--critical)" },
  { key: "cve_high", label: "High", color: "var(--serious)" },
  { key: "cve_medium", label: "Medium", color: "var(--warning)" },
];
const LEVEL_COLOR = {
  critical: "var(--critical)", high: "var(--serious)", medium: "var(--warning)", low: "var(--low)", info: "var(--accent)",
};

const state = {
  snapshot: null,
  deployments: [],
  stats: null,
  selected: [],          // ids sélectionnés pour la comparaison (max 2)
  drawer: null,          // { kind: "stage", id } | { kind: "deployment", id }
  detailCache: new Map() // id -> déploiement avec steps
};

const $ = (sel) => document.querySelector(sel);

// ---------- helpers DOM -------------------------------------------------------

function el(tag, props = {}, ...children) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(props)) {
    if (v == null || v === false) continue;
    if (k === "class") node.className = v;
    else if (k === "text") node.textContent = v;
    else if (k === "dataset") Object.assign(node.dataset, v);
    // via CSSOM : un attribut style="" serait bloqué par la CSP (style-src 'self')
    else if (k === "style") Object.assign(node.style, v);
    else if (k.startsWith("on")) node.addEventListener(k.slice(2), v);
    else node.setAttribute(k, v === true ? "" : v);
  }
  for (const c of children.flat()) {
    if (c == null || c === false) continue;
    node.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return node;
}

function safeLink(url, text) {
  try {
    const u = new URL(url);
    if (u.protocol === "https:") {
      return el("a", { href: u.href, target: "_blank", rel: "noopener noreferrer" }, text);
    }
  } catch { /* URL invalide : on affiche le texte seul */ }
  return document.createTextNode(text);
}

function statusBadge(status, label) {
  return el("span", { class: "status", dataset: { status } },
    el("span", { class: "status-icon", "aria-hidden": "true", text: STATUS_ICON[status] ?? "" }),
    el("span", { text: label ?? STATUS_LABEL[status] ?? status }));
}

const fmtDur = (s) => {
  if (s == null) return "—";
  s = Math.round(s);
  return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m${String(s % 60).padStart(2, "0")}s`;
};
const fmtDate = (iso) => new Date(iso).toLocaleString("fr-FR", { day: "2-digit", month: "2-digit", hour: "2-digit", minute: "2-digit" });
const shortDigest = (d) => (d ? d.replace("sha256:", "").slice(0, 12) : "");
const pct = (x) => `${(x * 100).toLocaleString("fr-FR", { maximumFractionDigits: 1 })} %`;

async function getJSON(url) {
  const res = await fetch(url, { headers: { Accept: "application/json" } });
  if (!res.ok) throw new Error(`${url}: HTTP ${res.status}`);
  return res.json();
}

// ---------- bandeau -------------------------------------------------------------

function renderBanner(s) {
  $("#prod-tag").textContent = s.prod.tag || "—";
  $("#prod-digest").textContent = s.prod.digest ? `@${shortDigest(s.prod.digest)}` : "";
  $("#prod-digest").title = s.prod.digest || "";
  const env = $("#env-badge");
  env.textContent = s.environment;
  env.dataset.env = s.environment;
  $("#source-badge").hidden = s.source !== "mock";
  document.title = `${s.prod.tag} · ContainerMaxxing`;
}

function setLive(stateName, text) {
  $("#live").dataset.state = stateName;
  $("#live-text").textContent = text;
}

// ---------- pipeline ------------------------------------------------------------

function renderPipeline(stages) {
  const list = $("#pipeline");
  stages.forEach((st, i) => {
    let li = list.children[i];
    if (!li) {
      // Le bouton est créé une seule fois puis mis à jour sur place,
      // pour garder le focus clavier pendant les mises à jour en direct.
      const btn = el("button", {
        type: "button", class: "stage", "aria-controls": "drawer", "aria-expanded": "false",
        onclick: () => openDrawer({ kind: "stage", id: st.id }),
      });
      li = el("li", {}, btn);
      list.append(li);
    }
    li.dataset.status = st.status;
    const btn = li.firstElementChild;
    btn.dataset.status = st.status;
    btn.dataset.stage = st.id;
    btn.setAttribute("aria-expanded", String(state.drawer?.kind === "stage" && state.drawer.id === st.id));
    btn.replaceChildren(
      el("div", { class: "stage-top" },
        el("span", { class: "stage-index", text: String(i + 1).padStart(2, "0") }),
        statusBadge(st.status)),
      el("div", { class: "stage-label", text: st.label }),
      el("div", { class: "stage-summary", text: st.summary }),
    );
  });
}

// ---------- panneau de détails -------------------------------------------------

function openDrawer(target) {
  state.drawer = target;
  $("#drawer").hidden = false;
  $("#scrim").hidden = false;
  renderDrawer();
  if (state.snapshot) renderPipeline(state.snapshot.stages);
  $("#drawer-close").focus();
}

function closeDrawer() {
  const prev = state.drawer;
  state.drawer = null;
  $("#drawer").hidden = true;
  $("#scrim").hidden = true;
  if (state.snapshot) renderPipeline(state.snapshot.stages);
  if (prev?.kind === "stage") document.querySelector(`.stage[data-stage="${prev.id}"]`)?.focus();
}

function renderDrawer() {
  const d = state.drawer;
  if (!d) return;
  if (d.kind === "stage") {
    const st = state.snapshot?.stages.find((s) => s.id === d.id);
    if (st) renderStageDrawer(st);
  } else {
    renderDeploymentDrawer(d.id);
  }
}

function section(title, ...content) {
  return el("section", { class: "drawer-section" }, el("h3", { text: title }), ...content);
}

function fieldsList(fields) {
  return el("dl", { class: "fields" }, fields.flatMap((f) => [
    el("dt", { text: f.label }),
    el("dd", { class: f.mono ? "mono" : null }, f.link ? safeLink(f.link, f.value) : f.value),
  ]));
}

function stepsList(steps) {
  return el("ol", { class: "steps" }, steps.map((s) => {
    const row = el("div", { class: "step-row" },
      statusBadge(s.status, s.name),
      el("span", { class: "step-dur", text: s.status === "pending" ? "—" : s.status === "running" ? "…" : fmtDur(s.duration_s) }));
    if (!s.log) return el("li", {}, row);
    return el("li", {}, el("details", {}, el("summary", {}, row), el("pre", { text: s.log })));
  }));
}

function scanCard(sc) {
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

function renderStageDrawer(st) {
  $("#drawer-eyebrow").textContent = `Étape · mise à jour ${new Date(st.updated_at).toLocaleTimeString("fr-FR")}`;
  $("#drawer-title").textContent = st.label;
  const body = [
    el("div", {}, statusBadge(st.status), el("p", { class: "hint", text: st.summary })),
    section("Détails", fieldsList(st.fields)),
  ];
  if (st.scans?.length) body.push(section("Analyses de sécurité", st.scans.map(scanCard)));
  if (st.steps?.length) body.push(section("Steps du job", stepsList(st.steps)));
  $("#drawer-body").replaceChildren(...body);
}

async function loadDeployment(id) {
  if (!state.detailCache.has(id)) {
    state.detailCache.set(id, await getJSON(`/api/deployments/${encodeURIComponent(id)}`));
  }
  return state.detailCache.get(id);
}

async function renderDeploymentDrawer(id) {
  $("#drawer-eyebrow").textContent = "Déploiement";
  $("#drawer-title").textContent = "chargement…";
  $("#drawer-body").replaceChildren();
  let d;
  try {
    d = await loadDeployment(id);
  } catch (e) {
    $("#drawer-title").textContent = "Erreur de chargement";
    return;
  }
  if (state.drawer?.kind !== "deployment" || state.drawer.id !== id) return;
  $("#drawer-eyebrow").textContent = `Déploiement #${d.id} · ${d.target}`;
  $("#drawer-title").textContent = d.tag;
  const fields = [
    { label: "Statut", value: STATUS_LABEL[d.status] ?? d.status },
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

function renderStats(s) {
  const tile = (label, value, sub) => el("div", { class: "stat" },
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

function renderLegend() {
  $("#cve-legend").replaceChildren(...SEVERITIES.map((s) =>
    el("span", { class: "legend-item" },
      el("span", { class: "swatch", style: { background: s.color }, "aria-hidden": "true" }), s.label)));
}

const SVG = "http://www.w3.org/2000/svg";
function svg(tag, attrs = {}, text) {
  const n = document.createElementNS(SVG, tag);
  for (const [k, v] of Object.entries(attrs)) n.setAttribute(k, v);
  if (text != null) n.textContent = text;
  return n;
}

function renderChart(items) {
  const data = [...items].reverse(); // chronologique
  const W = 1100, H = 220, m = { t: 12, r: 8, b: 40, l: 34 };
  const iw = W - m.l - m.r, ih = H - m.t - m.b;
  const totals = data.map((d) => d.cve_critical + d.cve_high + d.cve_medium);
  const maxV = Math.max(4, ...totals);
  const step = maxV <= 8 ? 2 : maxV <= 20 ? 5 : 10;
  const top = Math.ceil(maxV / step) * step;
  const y = (v) => m.t + ih - (v / top) * ih;
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
      const isTop = j === segs.length - 1;
      // 2px d'écart entre segments empilés ; extrémité haute arrondie.
      const gap = j > 0 ? 2 : 0;
      const h = Math.max(y0 - y1 - gap, 1);
      root.append(svg("path", { d: barPath(cx - bw / 2, y1, bw, h, isTop ? 3 : 0), fill: s.color }));
      base += s.v;
    });
    if (d.status === "failed") {
      root.append(svg("text", { class: "fail-mark", x: cx, y: y(base) - 6, "text-anchor": "middle" }, "✕"));
    }
    root.append(svg("text", { class: "tag-label", x: cx, y: H - m.b + 16, "text-anchor": "middle" }, d.tag));
    root.append(svg("text", { class: "axis-label", x: cx, y: H - m.b + 30, "text-anchor": "middle" }, d.target));
    const hit = svg("rect", { class: "hit" + (state.selected.includes(d.id) ? " selected" : ""), x: m.l + band * i, y: m.t, width: band, height: ih + 34 });
    hit.addEventListener("mousemove", (e) => showTooltip(e, d));
    hit.addEventListener("mouseleave", hideTooltip);
    hit.addEventListener("click", () => openDrawer({ kind: "deployment", id: d.id }));
    root.append(hit);
  });
  $("#cve-chart").replaceChildren(root);
}

function barPath(x, yTop, w, h, r) {
  r = Math.min(r, h, w / 2);
  const yb = yTop + h;
  return `M${x},${yb} V${yTop + r} Q${x},${yTop} ${x + r},${yTop} H${x + w - r} Q${x + w},${yTop} ${x + w},${yTop + r} V${yb} Z`;
}

function showTooltip(e, d) {
  const tt = $("#tooltip");
  const row = (k, v) => el("div", { class: "tt-row" }, el("span", { text: k }), el("span", { text: v }));
  tt.replaceChildren(
    el("div", { class: "tt-title", text: `${d.tag} · ${d.target}` }),
    row("Statut", STATUS_LABEL[d.status] ?? d.status),
    ...SEVERITIES.map((s) => row(s.label, String(d[s.key]))),
    row("Durée", fmtDur(d.duration_s)),
  );
  tt.hidden = false;
  const x = Math.min(e.clientX + 14, window.innerWidth - tt.offsetWidth - 8);
  tt.style.left = `${x}px`;
  tt.style.top = `${e.clientY + 14}px`;
}
function hideTooltip() { $("#tooltip").hidden = true; }

// ---------- historique : timeline ----------------------------------------------

function renderTimeline(items) {
  const sevCell = (v) => el("td", { class: "num" }, el("span", { class: v ? "sev" : "sev zero", text: String(v) }));
  const rows = items.map((d) => {
    const checked = state.selected.includes(d.id);
    const cb = el("input", {
      type: "checkbox", "aria-label": `Comparer ${d.tag}`, checked,
      onclick: (e) => e.stopPropagation(),
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
      el("td", { onclick: (e) => e.stopPropagation() }, d.run_url ? safeLink(d.run_url, "run ↗") : "—"),
    );
  });
  $("#timeline tbody").replaceChildren(...rows);
}

function toggleSelect(id) {
  const i = state.selected.indexOf(id);
  if (i >= 0) state.selected.splice(i, 1);
  else {
    state.selected.push(id);
    if (state.selected.length > 2) state.selected.shift();
  }
  renderHistory();
}

// ---------- comparaison ----------------------------------------------------------

async function renderCompare() {
  const box = $("#compare");
  if (state.selected.length !== 2) { box.hidden = true; return; }
  // A = la plus ancienne, B = la plus récente
  const [a, b] = await Promise.all(state.selected.map(loadDeployment));
  const [older, newer] = new Date(a.started_at) <= new Date(b.started_at) ? [a, b] : [b, a];

  const card = (label, va, vb, fmt = String, lowerIsBetter = true) => {
    const diff = vb - va;
    const trend = diff === 0 ? "same" : (diff < 0) === lowerIsBetter ? "better" : "worse";
    const sign = diff > 0 ? "+" : "";
    return el("div", { class: "delta-card" },
      el("span", { class: "eyebrow", text: label }),
      el("div", { class: "delta-values", text: `${fmt(va)} → ${fmt(vb)}` }),
      el("div", { class: "delta", dataset: { trend }, text: diff === 0 ? "identique" : `${sign}${fmt === String ? diff : fmtDur(Math.abs(diff))}` }));
  };

  const stepNames = [...new Set([...(older.steps ?? []), ...(newer.steps ?? [])].map((s) => s.name))];
  const stepOf = (d, n) => d.steps?.find((s) => s.name === n);
  const stepCell = (s) => el("td", { class: "num mono", text: s ? (s.status === "success" ? fmtDur(s.duration_s) : STATUS_LABEL[s.status]) : "—" });

  box.replaceChildren(
    el("div", { class: "compare-head" },
      el("h2", { text: `Comparaison ${older.tag} → ${newer.tag}` }),
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

function renderHistory() {
  renderChart(state.deployments);
  renderTimeline(state.deployments);
  renderCompare().catch(console.error);
}

async function refreshHistory() {
  const [list, stats] = await Promise.all([getJSON("/api/deployments?limit=30"), getJSON("/api/stats")]);
  state.deployments = list.items;
  state.stats = stats;
  state.selected = state.selected.filter((id) => list.items.some((d) => d.id === id));
  renderStats(stats);
  renderHistory();
}

function onSnapshot(s) {
  state.snapshot = s;
  renderBanner(s);
  renderPipeline(s.stages);
  if (state.drawer?.kind === "stage") renderDrawer();
}

function connect() {
  const es = new EventSource("/api/stream");
  es.addEventListener("open", () => setLive("open", "en direct"));
  es.addEventListener("status", (e) => onSnapshot(JSON.parse(e.data)));
  es.addEventListener("deployment", () => refreshHistory().catch(console.error));
  // EventSource se reconnecte tout seul ; on se contente d'afficher l'état.
  es.addEventListener("error", () => setLive("error", "reconnexion…"));
}

function init() {
  renderLegend();
  $("#drawer-close").addEventListener("click", closeDrawer);
  $("#scrim").addEventListener("click", closeDrawer);
  document.addEventListener("keydown", (e) => { if (e.key === "Escape" && state.drawer) closeDrawer(); });
  getJSON("/api/status").then(onSnapshot).catch(() => {});
  refreshHistory().catch(console.error);
  connect();
}

init();
