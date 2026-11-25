// Chaîne de déploiement animée.
//
// Le focus passe automatiquement d'une étape à l'autre :
//  - « direct »  : une release est en cours → on suit l'étape active (ou celle en échec) ;
//  - « visite »  : rien ne tourne → on fait défiler les étapes toutes les TOUR_MS ;
//  - « manuel »  : l'utilisateur a choisi une étape → on reprend l'auto après RESUME_MS
//                  (sauf si le suivi auto est désactivé).

import { $, el, statusBadge, fmtDur, LEVEL_COLOR, STATUS_LABEL, stageIcon, reducedMotion } from "./dom.js";
import type { RunInfo, Snapshot, Stage, StageID, Step, Status } from "./types.js";

const TOUR_MS = 5000;
const RESUME_MS = 20000;
const MAX_FIELDS = 5;

type Mode = "live" | "tour" | "manual" | null;

export interface Chain {
  /** frozen : release en pause ou rejouée, les chronomètres ne tournent plus. */
  update(snap: Snapshot, frozen?: boolean): void;
  focusStage(id: StageID): void;
}

export function createChain({ onOpenDetails }: { onOpenDetails: (id: StageID) => void }): Chain {
  const s = {
    snap: null as Snapshot | null,
    focus: null as StageID | null, // étape mise en avant
    mode: null as Mode,
    auto: true,                    // suivi automatique activé
    tourTimer: undefined as ReturnType<typeof setInterval> | undefined,
    resumeTimer: undefined as ReturnType<typeof setTimeout> | undefined,
    resumeAt: 0,
    rendered: "",                  // clé du dernier rendu du spotlight (évite de reconstruire pour rien)
    frozen: false,                 // pause présentateur ou rejeu
  };

  const nodes = $<HTMLOListElement>("#nodes");
  const spotlight = $("#spotlight");

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
  document.addEventListener("keydown", (e) => {
    const target = e.target as Element | null;
    if (!$("#drawer").hidden || target?.closest("input, textarea, select")) return;
    if (e.key === "ArrowLeft") step(-1);
    else if (e.key === "ArrowRight") step(1);
  });

  const ids = (): StageID[] => s.snap?.stages.map((st) => st.id) ?? [];

  function step(delta: number): void {
    const list = ids();
    if (!list.length) return;
    const i = Math.max(0, s.focus ? list.indexOf(s.focus) : 0);
    const next = list[(i + delta + list.length) % list.length];
    if (next) pick(next);
  }

  // Choix manuel : on fige le focus, puis on reprend le suivi auto plus tard.
  function pick(id: StageID): void {
    stopTour();
    s.mode = "manual";
    setFocus(id);
    clearTimeout(s.resumeTimer);
    if (s.auto) {
      s.resumeAt = Date.now() + RESUME_MS;
      s.resumeTimer = setTimeout(() => { s.mode = null; decide(); render(); }, RESUME_MS);
    }
    render();
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

  function startTour(): void {
    s.mode = "tour";
    if (!s.focus) {
      const first = ids()[0];
      if (first) setFocus(first);
    }
    restartTourBar();
    s.tourTimer = setInterval(() => {
      const list = ids();
      const next = list[((s.focus ? list.indexOf(s.focus) : -1) + 1) % list.length];
      if (next) setFocus(next);
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

  function setFocus(id: StageID): void {
    if (id === s.focus) return;
    s.focus = id;
    s.rendered = "";
    if (!reducedMotion()) {
      spotlight.classList.remove("swap");
      void spotlight.offsetWidth;
      spotlight.classList.add("swap");
    }
    const st = s.snap?.stages.find((x) => x.id === id);
    if (st) $("#chain-announce").textContent = `Étape ${st.label} : ${STATUS_LABEL[st.status]}`;
  }

  // --- rendu -------------------------------------------------------------------

  function render(): void {
    if (!s.snap) return;
    const { stages, run } = s.snap;
    const focused = stages.find((x) => x.id === s.focus) ?? stages[0];
    if (run) renderRunHeader(run);
    renderNodes(stages);
    renderTrack(stages, run);
    if (focused) renderSpotlight(focused, stages);
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
        const btn = el("button", { type: "button", class: "node", onclick: () => pick(st.id) },
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
      const done = steps.filter((x) => x.status === "success").length;
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
    $("#packet-tag").textContent = run?.tag ?? s.snap?.prod.tag ?? "";
    $("#packet-note").textContent = status === "success" ? "en prod" : status === "failed" ? "bloquée" : "";

    const focusIdx = s.focus ? Math.max(0, list.indexOf(s.focus)) : 0;
    $("#caret").style.left = `${center(focusIdx, n)}%`;
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

  function update(snap: Snapshot, frozen = false): void {
    s.snap = snap;
    s.frozen = frozen;
    decide();
    render();
  }

  function renderSpotlight(st: Stage, stages: Stage[]): void {
    // Reconstruit seulement si l'étape affichée a changé de contenu.
    const key = JSON.stringify([st.id, st.status, st.summary, st.steps, st.scans, st.fields, s.frozen]);
    if (key === s.rendered) return;
    s.rendered = key;
    const i = stages.indexOf(st);
    const steps = st.steps ?? [];

    const head = el("header", { class: "spot-head" },
      el("div", { class: "spot-icon", dataset: { status: st.status } }, stageIcon(st.id)),
      el("div", { class: "spot-title" },
        el("span", { class: "eyebrow", text: `Étape ${String(i + 1).padStart(2, "0")} / ${String(stages.length).padStart(2, "0")}` }),
        el("h3", { text: st.label }),
        el("div", { class: "spot-sub" }, statusBadge(st.status), el("span", { text: st.summary }))),
      el("button", { type: "button", class: "btn", onclick: () => onOpenDetails(st.id) }, "Détails"));

    spotlight.replaceChildren(head, el("div", { class: "spot-grid" },
      el("section", { class: "spot-col" }, el("h4", { text: "Steps" }), stepsTimeline(steps)),
      el("section", { class: "spot-col spot-term" }, el("h4", { text: "Journal" }), terminal(steps)),
      el("section", { class: "spot-col" }, el("h4", { text: "Infos clés" }), keyInfo(st))));
    const term = spotlight.querySelector<HTMLElement>(".terminal");
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
    for (const n of spotlight.querySelectorAll<HTMLElement>("[data-started]")) {
      const started = n.dataset.started;
      if (started) n.textContent = fmtDur((Date.now() - new Date(started).getTime()) / 1000);
    }
    if (s.mode === "manual" && s.auto) renderMode();
  }
  setInterval(tick, 1000);

  return { update, focusStage: pick };
}
