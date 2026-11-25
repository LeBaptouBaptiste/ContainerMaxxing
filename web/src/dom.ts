// Helpers DOM partagés.
// Règle de sécurité : aucune donnée n'est insérée via innerHTML ; tout passe
// par textContent / createElement, et les liens ne sont acceptés qu'en https.

import type { Level, StageID, Status } from "./types.js";

export const STATUS_LABEL: Record<Status, string> = {
  success: "OK", running: "en cours", failed: "échec", pending: "en attente", unknown: "inconnu", skipped: "ignoré",
};
const STATUS_ICON: Record<Status, string> = { success: "✓", failed: "✕", running: "", pending: "", unknown: "?", skipped: "–" };

export const LEVEL_COLOR: Record<Level, string> = {
  critical: "var(--critical)", high: "var(--serious)", medium: "var(--warning)", low: "var(--low)", info: "var(--accent)",
};

/** Sélectionne un élément qui doit exister dans index.html. */
export function $<T extends Element = HTMLElement>(sel: string): T {
  const node = document.querySelector<T>(sel);
  if (!node) throw new Error(`élément introuvable : ${sel}`);
  return node;
}

export type Child = Node | string | number | null | undefined | false | Child[];

export interface Props {
  class?: string | null;
  text?: string;
  dataset?: Record<string, string>;
  style?: Partial<CSSStyleDeclaration>;
  [attrOrHandler: string]: unknown;
}

export function el<K extends keyof HTMLElementTagNameMap>(tag: K, props: Props = {}, ...children: Child[]): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(props)) {
    if (v == null || v === false) continue;
    if (k === "class") node.className = String(v);
    else if (k === "text") node.textContent = String(v);
    else if (k === "dataset") Object.assign(node.dataset, v);
    // via CSSOM : un attribut style="" serait bloqué par la CSP (style-src 'self')
    else if (k === "style") Object.assign(node.style, v);
    else if (k.startsWith("on") && typeof v === "function") node.addEventListener(k.slice(2), v as EventListener);
    else node.setAttribute(k, v === true ? "" : String(v));
  }
  append(node, children);
  return node;
}

function append(node: Node, children: Child[]): void {
  for (const c of children) {
    if (c == null || c === false) continue;
    if (Array.isArray(c)) append(node, c);
    else node.appendChild(c instanceof Node ? c : document.createTextNode(String(c)));
  }
}

const SVG_NS = "http://www.w3.org/2000/svg";
export function svg(tag: string, attrs: Record<string, string | number> = {}, text?: string | number): SVGElement {
  const n = document.createElementNS(SVG_NS, tag);
  for (const [k, v] of Object.entries(attrs)) n.setAttribute(k, String(v));
  if (text != null) n.textContent = String(text);
  return n;
}

export function safeLink(url: string, text: string): HTMLAnchorElement | Text {
  try {
    const u = new URL(url);
    if (u.protocol === "https:") {
      return el("a", { href: u.href, target: "_blank", rel: "noopener noreferrer" }, text);
    }
  } catch { /* URL invalide : on affiche le texte seul */ }
  return document.createTextNode(text);
}

export function statusBadge(status: Status, label?: string): HTMLSpanElement {
  return el("span", { class: "status", dataset: { status } },
    el("span", { class: "status-icon", "aria-hidden": "true", text: STATUS_ICON[status] ?? "" }),
    el("span", { text: label ?? STATUS_LABEL[status] ?? status }));
}

export function fmtDur(s: number | null | undefined): string {
  if (s == null) return "—";
  const r = Math.round(s);
  return r < 60 ? `${r}s` : `${Math.floor(r / 60)}m${String(r % 60).padStart(2, "0")}s`;
}
export const fmtDate = (iso: string): string =>
  new Date(iso).toLocaleString("fr-FR", { day: "2-digit", month: "2-digit", hour: "2-digit", minute: "2-digit" });
export const shortDigest = (d: string): string => (d ? d.replace("sha256:", "").slice(0, 12) : "");
export const pct = (x: number): string => `${(x * 100).toLocaleString("fr-FR", { maximumFractionDigits: 1 })} %`;

/** Vrai si la touche est tapée dans un champ de saisie (raccourcis désactivés). */
export const typingIn = (e: Event): boolean =>
  e.target instanceof Element && e.target.closest("input, textarea, select") !== null;

export const reducedMotion = (): boolean => window.matchMedia("(prefers-reduced-motion: reduce)").matches;

export async function getJSON<T>(url: string): Promise<T> {
  const res = await fetch(url, { headers: { Accept: "application/json" } });
  if (!res.ok) throw new Error(`${url}: HTTP ${res.status}`);
  return res.json() as Promise<T>;
}

// Petites icônes d'étape (tracés SVG statiques, aucun contenu dynamique).
const ICONS: Record<StageID, string[]> = {
  image: ["M12 3 3 7.5l9 4.5 9-4.5L12 3Z", "M3 12l9 4.5 9-4.5", "M3 16.5 12 21l9-4.5"],
  phobos: ["M4 4h16v6H4z", "M4 14h16v6H4z", "M8 7h.01", "M8 17h.01"],
  compose: ["M4 4h7v7H4z", "M13 4h7v7h-7z", "M4 13h7v7H4z", "M13 13h7v7h-7z"],
  github: ["M6 3v12", "M18 9a3 3 0 1 0 0-6 3 3 0 0 0 0 6Z", "M6 21a3 3 0 1 0 0-6 3 3 0 0 0 0 6Z", "M18 9a9 9 0 0 1-9 9"],
  actions: ["M5 3h14a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2Z", "m10 8 6 4-6 4V8Z"],
  ghcr: ["M21 8 12 3 3 8v8l9 5 9-5V8Z", "M3 8l9 5 9-5", "M12 13v8"],
  caas: ["M7 18a4.5 4.5 0 0 1-.6-8.96A6 6 0 0 1 18 8.5a4.5 4.5 0 0 1-.5 9.5H7Z"],
};

export function stageIcon(id: StageID): SVGElement {
  const root = svg("svg", { viewBox: "0 0 24 24", "aria-hidden": "true", class: "stage-icon" });
  for (const d of ICONS[id] ?? ICONS.image) {
    root.append(svg("path", { d, fill: "none", stroke: "currentColor", "stroke-width": "1.7", "stroke-linecap": "round", "stroke-linejoin": "round" }));
  }
  return root;
}
