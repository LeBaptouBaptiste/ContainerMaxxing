// Chaîne de déploiement animée, avec une « caméra ».
//
// Le focus passe automatiquement d'une étape à l'autre :
//  - « direct »  : une release est en cours → on suit l'étape active (ou celle en échec) ;
//  - « visite »  : rien ne tourne → vue d'ensemble, puis chaque étape tour à tour ;
//  - « manuel »  : l'utilisateur a choisi une étape → on reprend l'auto après RESUME_MS
//                  (sauf si le suivi auto est désactivé).
//
// La caméra cadre l'étape en focus : la scène (piste + nœuds) est agrandie et
// décalée pour placer le nœud dans la moitié gauche, et le panneau de détails
// glisse à droite. Quand le focus change, on dézoome sur la vue d'ensemble,
// on laisse le paquet avancer, puis on zoome sur la nouvelle étape.

import { $, el, statusBadge, fmtDur, LEVEL_COLOR, STATUS_LABEL, stageIcon, reducedMotion, typingIn } from "./dom.js";
import type { RunInfo, Snapshot, Stage, StageID, Step, Status } from "./types.js";

const TOUR_MS = 7000;   // durée de chaque plan pendant la visite automatique
const RESUME_MS = 20000;
const MAX_FIELDS = 5;

// Caméra (doivent correspondre aux transitions CSS de .world et .zoom-panel).
const OUT_MS = 450;     // dézoom vers la vue d'ensemble
const HOLD_MS = 250;    // vue d'ensemble : le paquet avance vers l'étape suivante
const ZOOM = 3;         // grossissement maximal
const FRAME_X = 0.23;   // position horizontale du nœud cadré (part de la largeur)
const FRAME_FILL = 0.84; // part de la hauteur occupée par le bloc cadré (paquet → statut)

type Mode = "live" | "tour" | "manual" | null;

export interface Chain {
  /** frozen : release en pause ou rejouée, les chronomètres ne tournent plus. */
  update(snap: Snapshot, frozen?: boolean): void;
  focusStage(id: StageID): void;
}

export function createChain({ onOpenDetails }: { onOpenDetails: (id: StageID) => void }): Chain {
  const s = {
    snap: null as Snapshot | null,
    focus: null as StageID | null,  // étape mise en avant (null = vue d'ensemble)
    framed: null as StageID | null, // étape actuellement cadrée par la caméra
    target: null as StageID | null, // cadrage visé (peut être en cours de transition)
    mode: null as Mode,
    auto: true,                     // suivi automatique activé
    zoom: true,                     // false : vue d'ensemble figée, sans zoom
    tourTimer: undefined as ReturnType<typeof setInterval> | undefined,
    tourIdx: -1,
    resumeTimer: undefined as ReturnType<typeof setTimeout> | undefined,
    cameraTimer: undefined as ReturnType<typeof setTimeout> | undefined,
    resumeAt: 0,
    rendered: "",                   // clé du dernier rendu du panneau (évite de reconstruire pour rien)
    frozen: false,                  // pause présentateur ou rejeu
  };

  const viewport = $("#viewport");
  const world = $("#world");
  const nodes = $<HTMLOListElement>("#nodes");
  const panel = $("#spotlight");

  // --- contrôles -------------------------------------------------------------

  $("#chain-prev").addEventListener("click", () => step(-1));
  $("#chain-next").addEventListener("click", () => step(1));
  $("#chain-auto").addEventListener("click", () => {
    s.auto = !s.auto;
    clearTimeout(s.resumeTimer);
    if (s.auto) { s.mode = null; decide(); }
    else { stopTour(); s.mode = "manual"; }
    render();
  });
  $("#chain-overview").addEventListener("click", toggleOverview);
  $("#chain-fullscreen").addEventListener("click", toggleFullscreen);
  // Synchronise le bouton, y compris quand on sort du plein écran avec Échap.
  document.addEventListener("fullscreenchange", () => {
    $("#chain-fullscreen").setAttribute("aria-pressed", String(document.fullscreenElement !== null));
  });
  document.addEventListener("keydown", (e) => {
    if (!$("#drawer").hidden || typingIn(e) || e.ctrlKey || e.metaKey || e.altKey) return;
    if (e.key === "ArrowLeft") step(-1);
    else if (e.key === "ArrowRight") step(1);
    else if (e.key === "o" || e.key === "O") toggleOverview();
    else if (e.key === "f" || e.key === "F") toggleFullscreen();
  });

  // Plein écran du navigateur : nécessite un geste de l'utilisateur (clic ou touche).
  function toggleFullscreen(): void {
    if (document.fullscreenElement) {
      void document.exitFullscreen();
      return;
    }
    if (!s.zoom) toggleOverview(); // le plein écran se fait en mode scène
    document.documentElement.requestFullscreen().catch(() => { /* refusé par le navigateur */ });
  }
  // Le cadre change de taille (fenêtre, plein écran, passage page ↔ scène) :
  // on recalcule le cadrage sans animation.
  new ResizeObserver(() => applyCamera(s.framed, false)).observe(viewport);
  setStageLayout();

  const ids = (): StageID[] => s.snap?.stages.map((st) => st.id) ?? [];

  function step(delta: number): void {
    const list = ids();
    if (!list.length) return;
    const i = s.focus ? list.indexOf(s.focus) : delta > 0 ? -1 : list.length;
    const next = list[(i + delta + list.length) % list.length];
    if (next) pick(next);
  }

  // Choix manuel : on fige le focus, puis on reprend le suivi auto plus tard.
  function pick(id: StageID): void {
    stopTour();
    s.mode = "manual";
    if (!s.zoom) toggleOverview(); // cliquer une étape réactive le zoom
    setFocus(id);
    clearTimeout(s.resumeTimer);
    if (s.auto) {
      s.resumeAt = Date.now() + RESUME_MS;
      s.resumeTimer = setTimeout(() => { s.mode = null; decide(); render(); }, RESUME_MS);
    }
    render();
  }

  function toggleOverview(): void {
    s.zoom = !s.zoom;
    setStageLayout();
    frame();
  }

  // Zoom actif → mise en page « scène » : la chaîne occupe toute la fenêtre.
  // Vue d'ensemble → page classique, avec l'historique sous la chaîne.
  function setStageLayout(): void {
    document.body.classList.toggle("stage", s.zoom);
    const btn = $("#chain-overview");
    btn.setAttribute("aria-pressed", String(!s.zoom));
    btn.textContent = s.zoom ? "Vue d'ensemble" : "Mode scène";
    btn.title = s.zoom ? "Revenir à la page avec l'historique (O)" : "Chaîne en plein écran avec zoom (O)";
  }

  // --- logique de focus ------------------------------------------------------

  function decide(): void {
    if (s.mode === "manual" || !s.snap) return;
    const run = s.snap.run;
    if (run && (run.status === "running" || run.status === "failed") && run.stage) {
      stopTour();
      s.mode = "live";
      setFocus(run.stage);
    } else if (s.mode !== "tour") {
      startTour();
    }
  }

  // Visite : vue d'ensemble, puis chaque étape, puis de nouveau la vue d'ensemble…
  function startTour(): void {
    s.mode = "tour";
    s.tourIdx = -1;
    setFocus(null);
    restartTourBar();
    s.tourTimer = setInterval(() => {
      const list = ids();
      s.tourIdx = s.tourIdx + 1 >= list.length ? -1 : s.tourIdx + 1;
      setFocus(s.tourIdx < 0 ? null : list[s.tourIdx] ?? null);
      restartTourBar();
      render();
    }, TOUR_MS);
  }

  function stopTour(): void {
    clearInterval(s.tourTimer);
    s.tourTimer = undefined;
  }

  function restartTourBar(): void {
    const bar = $("#tour-bar");
    bar.classList.remove("run");
    void bar.offsetWidth; // force le redémarrage de l'animation CSS
    bar.classList.add("run");
  }

  function setFocus(id: StageID | null): void {
    if (id === s.focus) return;
    s.focus = id;
    const st = id ? s.snap?.stages.find((x) => x.id === id) : undefined;
    $("#chain-announce").textContent = st ? `Étape ${st.label} : ${STATUS_LABEL[st.status]}` : "Vue d'ensemble de la chaîne";
    frame();
  }

  // --- caméra -------------------------------------------------------------------

  // Décide du cadrage : zoom direct, ou dézoom → pause → rezoom entre deux étapes.
  function frame(): void {
    const target = s.zoom ? s.focus : null;
    if (target === s.target) return;
    s.target = target;
    clearTimeout(s.cameraTimer);
    const animate = !reducedMotion();
    if (s.framed && target && s.framed !== target && animate) {
      show(null);
      s.cameraTimer = setTimeout(() => show(target), OUT_MS + HOLD_MS);
    } else {
      show(target);
    }
  }

  function show(id: StageID | null): void {
    s.framed = id;
    s.rendered = "";
    viewport.dataset.zoom = id ? "in" : "out";
    applyCamera(id, true);
    render();
  }

  // Position d'un élément dans la scène, sans tenir compte du transform courant.
  function offsetIn(node: HTMLElement): { x: number; y: number } {
    let x = 0, y = 0;
    let n: HTMLElement | null = node;
    while (n && n !== world) {
      x += n.offsetLeft;
      y += n.offsetTop;
      n = n.offsetParent as HTMLElement | null;
    }
    return { x, y };
  }

  function applyCamera(id: StageID | null, animated: boolean): void {
    world.classList.toggle("no-anim", !animated);
    const vw = viewport.clientWidth, vh = viewport.clientHeight;
    const btn = id ? nodes.querySelector<HTMLElement>(`.node[data-stage="${id}"]`) : null;
    const ring = btn?.querySelector<HTMLElement>(".node-ring");
    if (!btn || !ring) {
      // Vue d'ensemble : la scène entière, centrée verticalement.
      world.style.transform = `translate(0px, ${Math.max(0, (vh - world.offsetHeight) / 2)}px) scale(1)`;
      return;
    }
    // On cadre tout le bloc de l'étape : du haut du paquet au bas du statut.
    const packet = $("#packet");
    const top = Math.min(offsetIn(packet).y, offsetIn(btn).y);
    const bottom = offsetIn(btn).y + btn.offsetHeight;
    const r = offsetIn(ring);
    const cx = r.x + ring.offsetWidth / 2;
    const k = Math.min(ZOOM, (vh * FRAME_FILL) / (bottom - top));
    const ty = (vh - k * (bottom - top)) / 2 - k * top;
    world.style.transform = `translate(${vw * FRAME_X - k * cx}px, ${ty}px) scale(${k})`;
  }

  // --- rendu -------------------------------------------------------------------

  function update(snap: Snapshot, frozen = false): void {
    const first = !s.snap;
    s.snap = snap;
    s.frozen = frozen;
    render();      // crée les nœuds avant de cadrer la caméra
    decide();
    if (first) applyCamera(s.framed, false);
    render();
  }

  function render(): void {
    if (!s.snap) return;
    const { stages, run } = s.snap;
    if (run) renderRunHeader(run);
    renderNodes(stages);
    renderTrack(stages, run);
    const framed = s.framed ? stages.find((x) => x.id === s.framed) : undefined;
    if (framed) renderPanel(framed, stages);
    renderMode();
  }

  const RUN_LABEL: Partial<Record<Status, string>> = {
    running: "release en cours", failed: "release bloquée", success: "release déployée",
  };

  function renderRunHeader(run: RunInfo): void {
    $("#run-tag").textContent = run.tag;
    $("#run-number").textContent = `run #${run.number}`;
    $("#run-status").replaceChildren(statusBadge(run.status, RUN_LABEL[run.status]));
    $("#run-step").textContent = run.status === "success" ? "toutes les étapes sont passées" : run.step_name ?? "";
    $("#run-count").textContent = `${run.steps_done} / ${run.step_count} steps`;
    const bar = $("#run-progress");
    bar.style.width = `${(run.steps_done / run.step_count) * 100}%`;
    bar.dataset.status = run.status;
  }

  function renderNodes(stages: Stage[]): void {
    stages.forEach((st, i) => {
      let li = nodes.children[i] as HTMLLIElement | undefined;
      if (!li) {
        // Créé une seule fois puis mis à jour sur place : garde le focus clavier.
        const btn = el("button", { type: "button", class: "node", dataset: { stage: st.id }, onclick: () => pick(st.id) },
          el("span", { class: "node-ring" }, el("span", { class: "node-core" }, stageIcon(st.id))),
          el("span", { class: "node-index", text: String(i + 1).padStart(2, "0") }),
          el("span", { class: "node-label", text: st.label }),
          el("span", { class: "node-status" }));
        li = el("li", {}, btn);
        nodes.append(li);
      }
      const btn = li.firstElementChild as HTMLButtonElement;
      const focused = st.id === s.focus;
      btn.dataset.status = st.status;
      btn.dataset.focus = String(focused);
      btn.setAttribute("aria-pressed", String(focused));
      btn.setAttribute("aria-label", `${st.label} : ${STATUS_LABEL[st.status]}`);
      const steps = st.steps ?? [];
      const done = steps.filter((x) => x.status === "success" || x.status === "skipped").length;
      const p = st.status === "success" ? 100 : steps.length ? (done / steps.length) * 100 : 0;
      btn.style.setProperty("--p", String(p));
      btn.querySelector(".node-status")?.replaceChildren(statusBadge(st.status));
    });
  }

  const center = (i: number, n: number): number => ((i + 0.5) / n) * 100;

  function renderTrack(stages: Stage[], run: RunInfo | undefined): void {
    const n = stages.length;
    const list = stages.map((x) => x.id);
    let at = n - 1; // au repos : le paquet est arrivé en prod
    if (run?.stage && run.status !== "success") at = Math.max(0, list.indexOf(run.stage));
    const status = run?.status ?? "success";
    $("#track").dataset.status = status;

    // La ligne va du centre du premier nœud au centre du dernier.
    const fill = status === "success" ? 1 : at / (n - 1);
    $("#track-fill").style.width = `${fill * 100}%`;

    const packet = $("#packet");
    packet.style.left = `${center(at, n)}%`;
    packet.dataset.status = status;
    // Pendant un zoom, le paquet n'est visible que s'il est sur l'étape cadrée.
    packet.dataset.away = String(s.framed !== null && list[at] !== s.framed);
    $("#packet-tag").textContent = run?.tag ?? s.snap?.prod.tag ?? "";
    $("#packet-note").textContent = status === "success" ? "en prod" : status === "failed" ? "bloquée" : "";
  }

  function renderMode(): void {
    const label = $("#chain-mode");
    const auto = $("#chain-auto");
    auto.setAttribute("aria-pressed", String(s.auto));
    auto.textContent = s.auto ? "❚❚ Suivi auto" : "▶ Suivi auto";
    $("#tour-bar").hidden = s.mode !== "tour";
    if (s.mode === "live") label.textContent = "suivi en direct";
    else if (s.mode === "tour") label.textContent = "visite automatique";
    else if (s.auto) label.textContent = `sélection manuelle · reprise auto dans ${Math.max(0, Math.ceil((s.resumeAt - Date.now()) / 1000))} s`;
    else label.textContent = "suivi auto en pause";
  }

  // --- panneau de détails (moitié droite quand on zoome) -------------------------

  function renderPanel(st: Stage, stages: Stage[]): void {
    // Reconstruit seulement si l'étape affichée a changé de contenu.
    const key = JSON.stringify([st.id, st.status, st.summary, st.steps, st.scans, st.fields, s.frozen]);
    if (key === s.rendered) return;
    s.rendered = key;
    const i = stages.indexOf(st);
    const steps = st.steps ?? [];

    const head = el("header", { class: "zp-head" },
      el("div", {},
        el("span", { class: "eyebrow", text: `Étape ${String(i + 1).padStart(2, "0")} / ${String(stages.length).padStart(2, "0")}` }),
        el("h3", { text: st.label }),
        el("div", { class: "zp-sub" }, statusBadge(st.status), el("span", { text: st.summary }))),
      el("button", { type: "button", class: "btn", onclick: () => onOpenDetails(st.id) }, "Détails"));

    panel.replaceChildren(head, el("div", { class: "zp-grid" },
      el("section", { class: "zp-col" }, el("h4", { text: "Steps" }), stepsTimeline(steps)),
      el("section", { class: "zp-col" }, el("h4", { text: "Infos clés" }), keyInfo(st)),
      el("section", { class: "zp-col zp-term" }, el("h4", { text: "Journal" }), terminal(steps))));
    const term = panel.querySelector<HTMLElement>(".terminal");
    if (term) term.scrollTop = term.scrollHeight;
    tick();
  }

  function stepsTimeline(steps: Step[]): HTMLElement {
    if (!steps.length) return el("p", { class: "hint", text: "Aucun step." });
    return el("ol", { class: "spot-steps" }, steps.map((x) => el("li", { dataset: { status: x.status } },
      statusBadge(x.status, x.name),
      x.status === "running" && x.started_at && !s.frozen
        ? el("span", { class: "step-dur live", dataset: { started: x.started_at }, text: "0s" })
        : el("span", { class: "step-dur", text: stepDuration(x) }))));
  }

  function stepDuration(x: Step): string {
    if (x.status === "running") return "en pause"; // chronomètre figé (pause ou rejeu)
    if (x.status === "pending" || x.status === "skipped") return "—";
    return fmtDur(x.duration_s);
  }

  function terminal(steps: Step[]): HTMLElement {
    const lines: HTMLElement[] = [];
    for (const x of steps) {
      if (x.status === "pending") continue;
      lines.push(el("div", { class: "t-cmd", dataset: { status: x.status } }, el("span", { class: "t-prompt", text: "$ " }), x.name));
      if (x.log) for (const l of x.log.split("\n")) lines.push(el("div", { class: "t-out", text: l }));
      if (x.status === "running") lines.push(el("div", { class: "t-out" }, el("span", { class: "t-cursor", "aria-hidden": "true" })));
    }
    if (!lines.length) lines.push(el("div", { class: "t-out muted", text: "en attente…" }));
    return el("div", { class: "terminal", role: "log" }, lines);
  }

  function keyInfo(st: Stage): HTMLElement[] {
    const out: HTMLElement[] = [el("dl", { class: "fields" }, (st.fields ?? []).slice(0, MAX_FIELDS).flatMap((f) => [
      el("dt", { text: f.label }),
      el("dd", { class: f.mono ? "mono clip" : null, title: f.value, text: f.value }),
    ]))];
    for (const sc of st.scans ?? []) {
      out.push(el("div", { class: "spot-scan" },
        el("div", { class: "spot-scan-head" }, el("b", { text: sc.tool }), statusBadge(sc.status)),
        sc.counts?.length
          ? el("div", { class: "counts" }, sc.counts.map((c) => el("span", { class: "count" },
            el("span", { class: "swatch", style: { background: LEVEL_COLOR[c.level] ?? "var(--low)" }, "aria-hidden": "true" }),
            `${c.label} `, el("b", { text: String(c.value) }))))
          : el("span", { class: "hint", text: sc.summary })));
    }
    return out;
  }

  // Compteurs en direct (durée du step en cours, compte à rebours de reprise).
  function tick(): void {
    for (const n of panel.querySelectorAll<HTMLElement>("[data-started]")) {
      const started = n.dataset.started;
      if (started) n.textContent = fmtDur((Date.now() - new Date(started).getTime()) / 1000);
    }
    if (s.mode === "manual" && s.auto) renderMode();
  }
  setInterval(tick, 1000);

  return { update, focusStage: pick };
}
