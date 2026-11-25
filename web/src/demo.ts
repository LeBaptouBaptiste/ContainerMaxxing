// Mode démo / présentateur.
//
// - Scénarios : lance une release qui illustre un contrôle de sécurité précis.
// - Pause / lecture / avancer : pilotent la release côté serveur (tous les
//   navigateurs ouverts voient la même chose).
// - Reculer : rejoue côté client les états déjà reçus de la release en cours,
//   sans rien changer sur le serveur.
// - Présentation : agrandit la chaîne et masque l'historique pour le projecteur.
//
// Les contrôles n'apparaissent que si le serveur les active (snapshot.demo).

import { $, el } from "./dom.js";
import type { DemoInfo, Snapshot } from "./types.js";

const TOKEN_KEY = "cmx-demo-token";   // sessionStorage : effacé à la fermeture de l'onglet
const PRESENT_KEY = "cmx-presenter";

export interface Demo {
  /** Reçoit chaque snapshot en direct ; décide quoi afficher (direct ou rejeu). */
  ingest(live: Snapshot): void;
}

export function createDemo({ display }: { display: (snap: Snapshot, frozen: boolean) => void }): Demo {
  const s = {
    live: null as Snapshot | null,
    history: [] as Snapshot[], // états successifs de la release en cours
    view: null as number | null, // index rejoué, null = direct
    busy: false,
  };

  const bar = $("#demo-bar");
  const msg = $("#demo-msg");
  const auth = $<HTMLFormElement>("#demo-auth");
  const tokenInput = $<HTMLInputElement>("#demo-token");

  // --- présentation (local au navigateur) -----------------------------------------

  function setPresenter(on: boolean): void {
    document.body.classList.toggle("presenter", on);
    $("#demo-present").setAttribute("aria-pressed", String(on));
    sessionStorage.setItem(PRESENT_KEY, on ? "1" : "");
  }
  setPresenter(sessionStorage.getItem(PRESENT_KEY) === "1");

  // --- commandes serveur ------------------------------------------------------------

  async function command(path: string, body: object = {}): Promise<void> {
    if (s.busy) return;
    s.busy = true;
    try {
      const headers: Record<string, string> = { "Content-Type": "application/json" };
      const token = sessionStorage.getItem(TOKEN_KEY);
      if (token) headers.Authorization = `Bearer ${token}`;
      const res = await fetch(`/api/demo/${path}`, { method: "POST", headers, body: JSON.stringify(body) });
      if (res.status === 401) {
        sessionStorage.removeItem(TOKEN_KEY);
        auth.hidden = false;
        tokenInput.focus();
        note("Jeton de démo requis.");
        return;
      }
      if (!res.ok) note(`Commande refusée (HTTP ${res.status}).`);
    } catch {
      note("Serveur injoignable.");
    } finally {
      s.busy = false;
    }
  }

  function note(text: string): void {
    msg.textContent = text;
  }

  const paused = (): boolean => s.live?.demo?.paused ?? false;

  function trigger(id: string): void {
    s.view = null;
    void command("release", { scenario: id });
  }

  function togglePlay(): void {
    void command(paused() ? "play" : "pause");
  }

  function forward(): void {
    if (s.view !== null) {
      // En rejeu : on avance dans les états déjà reçus, puis on revient au direct.
      s.view = s.view + 1 >= s.history.length - 1 ? null : s.view + 1;
      show();
      return;
    }
    void command("step");
  }

  function back(): void {
    if (s.history.length < 2) return;
    const from = s.view ?? s.history.length - 1;
    s.view = Math.max(0, from - 1);
    show();
  }

  // --- écouteurs --------------------------------------------------------------------

  $("#demo-back").addEventListener("click", back);
  $("#demo-fwd").addEventListener("click", forward);
  $("#demo-play").addEventListener("click", togglePlay);
  $("#demo-present").addEventListener("click", () => setPresenter(!document.body.classList.contains("presenter")));
  $("#replay-live").addEventListener("click", () => { s.view = null; show(); });
  auth.addEventListener("submit", (e) => {
    e.preventDefault();
    const v = tokenInput.value.trim();
    if (!v) return;
    sessionStorage.setItem(TOKEN_KEY, v);
    tokenInput.value = "";
    auth.hidden = true;
    note("Jeton enregistré pour cet onglet.");
  });

  document.addEventListener("keydown", (e) => {
    if (bar.hidden || !$("#drawer").hidden || e.ctrlKey || e.metaKey || e.altKey) return;
    if ((e.target as Element | null)?.closest("input, textarea, select")) return;
    const scenarios = s.live?.demo?.scenarios ?? [];
    if (e.key === " ") {
      e.preventDefault(); // pas de défilement ni de clic sur le bouton focalisé
      if (e.shiftKey) back(); else forward();
    } else if (e.key === "p" || e.key === "P") {
      togglePlay();
    } else if (e.key === "f" || e.key === "F") {
      setPresenter(!document.body.classList.contains("presenter"));
    } else if (/^[1-9]$/.test(e.key)) {
      const sc = scenarios[Number(e.key) - 1];
      if (sc) trigger(sc.id);
    }
  });

  // --- rendu --------------------------------------------------------------------------

  function renderBar(demo: DemoInfo): void {
    bar.hidden = false;
    const wrap = $("#demo-scenarios");
    if (wrap.childElementCount !== demo.scenarios.length) {
      wrap.replaceChildren(...demo.scenarios.map((sc, i) => el("button", {
        type: "button", class: "btn scenario", dataset: { id: sc.id }, title: sc.description,
        onclick: () => trigger(sc.id),
      }, el("kbd", { text: String(i + 1) }), sc.label)));
    }
    const running = s.live?.run?.status === "running";
    for (const b of wrap.querySelectorAll<HTMLButtonElement>(".scenario")) {
      b.setAttribute("aria-pressed", String(b.dataset.id === demo.scenario && running));
    }
    const play = $("#demo-play");
    play.textContent = demo.paused ? "▶ Lecture" : "❚❚ Pause";
    play.setAttribute("aria-pressed", String(demo.paused));
    if (demo.auth_required && !sessionStorage.getItem(TOKEN_KEY)) auth.hidden = false;

    const current = demo.scenarios.find((x) => x.id === demo.scenario);
    if (current) note(`Scénario « ${current.label} » : ${current.description}`);
  }

  function show(): void {
    const replay = s.view !== null ? s.history[s.view] : undefined;
    const banner = $("#replay-banner");
    if (replay?.run) {
      banner.hidden = false;
      $("#replay-pos").textContent = `step ${replay.run.steps_done} / ${replay.run.step_count}`;
      display(replay, true);
    } else {
      s.view = null;
      banner.hidden = true;
      if (s.live) display(s.live, paused());
    }
  }

  // Conserve les états successifs de la release en cours (pour reculer).
  function record(live: Snapshot): void {
    const run = live.run;
    if (!run) return;
    const last = s.history.at(-1)?.run;
    if (!last || last.number !== run.number) {
      s.history = [live];
      s.view = null; // nouvelle release : on revient au direct
      return;
    }
    if (last.steps_done !== run.steps_done || last.status !== run.status) s.history.push(live);
    else s.history[s.history.length - 1] = live;
  }

  function ingest(live: Snapshot): void {
    s.live = live;
    if (live.demo) {
      record(live);
      renderBar(live.demo);
    } else {
      bar.hidden = true;
      s.view = null;
    }
    show();
  }

  return { ingest };
}
