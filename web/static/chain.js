// Chaîne de déploiement animée.
//
// Le focus passe automatiquement d'une étape à l'autre :
//  - « direct »  : une release est en cours → on suit l'étape active (ou celle en échec) ;
//  - « visite »  : rien ne tourne → on fait défiler les étapes toutes les TOUR_MS ;
//  - « manuel »  : l'utilisateur a choisi une étape → on reprend l'auto après RESUME_MS
//                  (sauf si le suivi auto est désactivé).

import { $, el, statusBadge, fmtDur, LEVEL_COLOR, STATUS_LABEL, stageIcon, reducedMotion } from "./dom.js";

const TOUR_MS = 5000;
const RESUME_MS = 20000;
const MAX_FIELDS = 5;

export function createChain({ onOpenDetails }) {
  const s = {
    snap: null,
    focus: null,       // id de l'étape mise en avant
    mode: null,        // "live" | "tour" | "manual"
    auto: true,        // suivi automatique activé
    tourTimer: null,
    resumeTimer: null,
    resumeAt: 0,
    rendered: "",      // clé du dernier rendu du spotlight (évite de reconstruire pour rien)
  };

  const nodes = $("#nodes");
  const spotlight = $("#spotlight");

  // --- contrôles -------------------------------------------------------------

  $("#chain-prev").addEventListener("click", () => step(-1));
  $("#chain-next").addEventListener("click", () => step(1));
  $("#chain-auto").addEventListener("click", () => {
    s.auto = !s.auto;
    if (s.auto) { s.mode = null; clearTimeout(s.resumeTimer); decide(); }
    else { stopTour(); clearTimeout(s.resumeTimer); s.mode = "manual"; }
    render();
  });
  document.addEventListener("keydown", (e) => {
    if (!$("#drawer").hidden || e.target.closest("input, textarea, select")) return;
    if (e.key === "ArrowLeft") step(-1);
    else if (e.key === "ArrowRight") step(1);
  });

  function ids() { return s.snap?.stages.map((st) => st.id) ?? []; }

  function step(delta) {
    const list = ids();
    if (!list.length) return;
    const i = Math.max(0, list.indexOf(s.focus));
    pick(list[(i + delta + list.length) % list.length]);
  }

  // Choix manuel : on fige le focus, puis on reprend le suivi auto plus tard.
  function pick(id) {
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

  function decide() {
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

  function startTour() {
    s.mode = "tour";
    if (!s.focus) setFocus(ids()[0]);
    restartTourBar();
    s.tourTimer = setInterval(() => {
      const list = ids();
      setFocus(list[(list.indexOf(s.focus) + 1) % list.length]);
      restartTourBar();
      render();
    }, TOUR_MS);
  }

  function stopTour() {
    clearInterval(s.tourTimer);
    s.tourTimer = null;
  }

  function restartTourBar() {
    const bar = $("#tour-bar");
    bar.classList.remove("run");
    void bar.offsetWidth; // force le redémarrage de l'animation CSS
    bar.classList.add("run");
  }

  function setFocus(id) {
    if (!id || id === s.focus) return;
    const list = ids();
    const dir = list.indexOf(id) >= list.indexOf(s.focus) ? "next" : "prev";
    s.focus = id;
    s.rendered = "";
    spotlight.dataset.dir = dir;
    if (!reducedMotion()) {
      spotlight.classList.remove("swap");
      void spotlight.offsetWidth;
      spotlight.classList.add("swap");
    }
    const st = s.snap?.stages.find((x) => x.id === id);
    if (st) $("#chain-announce").textContent = `Étape ${st.label} : ${STATUS_LABEL[st.status] ?? st.status}`;
  }

  // --- rendu -------------------------------------------------------------------

  function update(snap) {
    s.snap = snap;
    decide();
    render();
  }

  function render() {
    if (!s.snap) return;
    const stages = s.snap.stages;
    const run = s.snap.run;
    renderRunHeader(run);
    renderNodes(stages);
    renderTrack(stages, run);
    renderSpotlight(stages.find((x) => x.id === s.focus) ?? stages[0], stages);
    renderMode();
  }

  function renderRunHeader(run) {
    if (!run) return;
    $("#run-tag").textContent = run.tag;
    $("#run-number").textContent = `run #${run.number}`;
    $("#run-status").replaceChildren(statusBadge(run.status, {
      running: "release en cours", failed: "release bloquée", success: "release déployée",
    }[run.status]));
    const stepText = run.status === "success" ? "toutes les étapes sont passées" : run.step_name ?? "";
    $("#run-step").textContent = stepText;
    $("#run-count").textContent = `${run.steps_done} / ${run.step_count} steps`;
    const bar = $("#run-progress");
    bar.style.width = `${(run.steps_done / run.step_count) * 100}%`;
    bar.dataset.status = run.status;
  }

  function renderNodes(stages) {
    stages.forEach((st, i) => {
      let li = nodes.children[i];
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
      const btn = li.firstElementChild;
      btn.dataset.status = st.status;
      btn.dataset.focus = String(st.id === s.focus);
      btn.setAttribute("aria-pressed", String(st.id === s.focus));
      btn.setAttribute("aria-label", `${st.label} : ${STATUS_LABEL[st.status] ?? st.status}`);
      const steps = st.steps ?? [];
      const done = steps.filter((x) => x.status === "success").length;
      const p = steps.length ? (done / steps.length) * 100 : st.status === "success" ? 100 : 0;
      btn.style.setProperty("--p", st.status === "success" ? 100 : p);
      btn.querySelector(".node-status").replaceChildren(statusBadge(st.status));
    });
  }

  function center(i, n) { return ((i + 0.5) / n) * 100; }

  function renderTrack(stages, run) {
    const n = stages.length;
    const list = stages.map((x) => x.id);
    let at = n - 1; // au repos : le paquet est arrivé en prod
    if (run && run.stage && run.status !== "success") at = Math.max(0, list.indexOf(run.stage));
    const track = $("#track");
    track.dataset.status = run?.status ?? "success";

    // La ligne va du centre du premier nœud au centre du dernier.
    const fill = run?.status === "success" ? 1 : at / (n - 1);
    $("#track-fill").style.width = `${fill * 100}%`;

    const packet = $("#packet");
    packet.style.left = `${center(at, n)}%`;
    packet.dataset.status = run?.status ?? "success";
    $("#packet-tag").textContent = run?.tag ?? s.snap.prod.tag;
    $("#packet-note").textContent = run?.status === "success" ? "en prod" : run?.status === "failed" ? "bloquée" : "";

    $("#caret").style.left = `${center(Math.max(0, list.indexOf(s.focus)), n)}%`;
  }

  function renderMode() {
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

  function renderSpotlight(st, stages) {
    // Reconstruit seulement si l'étape affichée a changé de contenu.
    const key = JSON.stringify([st.id, st.status, st.summary, st.steps, st.scans, st.fields]);
    if (key === s.rendered) return;
    s.rendered = key;
    const i = stages.indexOf(st);

    const head = el("header", { class: "spot-head" },
      el("div", { class: "spot-icon", dataset: { status: st.status } }, stageIcon(st.id)),
      el("div", { class: "spot-title" },
        el("span", { class: "eyebrow", text: `Étape ${String(i + 1).padStart(2, "0")} / ${String(stages.length).padStart(2, "0")}` }),
        el("h3", { text: st.label }),
        el("div", { class: "spot-sub" }, statusBadge(st.status), el("span", { text: st.summary }))),
      el("button", { type: "button", class: "btn", onclick: () => onOpenDetails(st.id) }, "Tous les détails →"));

    spotlight.replaceChildren(head, el("div", { class: "spot-grid" },
      el("section", { class: "spot-col" }, el("h4", { text: "Steps" }), stepsTimeline(st.steps ?? [])),
      el("section", { class: "spot-col spot-term" }, el("h4", { text: "Journal" }), terminal(st.steps ?? [])),
      el("section", { class: "spot-col" }, el("h4", { text: "Infos clés" }), keyInfo(st))));
    const term = spotlight.querySelector(".terminal");
    if (term) term.scrollTop = term.scrollHeight;
    tick();
  }

  function stepsTimeline(steps) {
    if (!steps.length) return el("p", { class: "hint", text: "Aucun step." });
    return el("ol", { class: "spot-steps" }, steps.map((x) => el("li", { dataset: { status: x.status } },
      statusBadge(x.status, x.name),
      x.status === "running" && x.started_at
        ? el("span", { class: "step-dur live", dataset: { started: x.started_at }, text: "0s" })
        : el("span", { class: "step-dur", text: x.status === "pending" ? "—" : fmtDur(x.duration_s) }))));
  }

  function terminal(steps) {
    const lines = [];
    for (const x of steps) {
      if (x.status === "pending") continue;
      lines.push(el("div", { class: "t-cmd", dataset: { status: x.status } }, el("span", { class: "t-prompt", text: "$ " }), x.name));
      if (x.log) for (const l of x.log.split("\n")) lines.push(el("div", { class: "t-out", text: l }));
      if (x.status === "running") lines.push(el("div", { class: "t-out" }, el("span", { class: "t-cursor", "aria-hidden": "true" })));
    }
    if (!lines.length) lines.push(el("div", { class: "t-out muted", text: "en attente…" }));
    return el("div", { class: "terminal", role: "log" }, lines);
  }

  function keyInfo(st) {
    const out = [el("dl", { class: "fields" }, st.fields.slice(0, MAX_FIELDS).flatMap((f) => [
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
  function tick() {
    for (const n of spotlight.querySelectorAll("[data-started]")) {
      n.textContent = fmtDur((Date.now() - new Date(n.dataset.started)) / 1000);
    }
    if (s.mode === "manual" && s.auto) renderMode();
  }
  setInterval(tick, 1000);

  return { update, focusStage: pick };
}
