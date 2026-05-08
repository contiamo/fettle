import { targetEl } from "./dom";
import {
  initFindingFilters,
  refreshRichSelectDisplays,
  refreshSelection,
} from "./findings";
import { initResizable } from "./resizable";

// --- Theme toggle ---
// Applies the selected theme and persists the choice in localStorage.
// Triggered by clicks on any element with data-set-theme="light|dark|system"
// (rendered by themeMenu in layout.templ).

function setTheme(mode: string) {
  localStorage.setItem("theme", mode);
  const h = document.documentElement;
  const dark =
    mode === "dark" ||
    (mode === "system" && matchMedia("(prefers-color-scheme:dark)").matches);
  h.classList.toggle("dark", dark);
}

document.addEventListener("click", (e) => {
  const t = targetEl(e);
  if (!t) return;

  // Theme dropdown items.
  const themeEl = t.closest<HTMLElement>("[data-set-theme]");
  if (themeEl) {
    setTheme(themeEl.getAttribute("data-set-theme")!);
    return;
  }

  // Whole-row click navigation. The row carries data-row-link="<href>".
  // We bail out when the click landed on a real <a> or <button> so
  // middle-click / right-click / "open in new tab" on the in-row link
  // (and any future inline buttons) keep working. List rows in the
  // workspace use HTMX directly, so we skip them here.
  if (t.closest("a") || t.closest("button") || t.closest("input") || t.closest("label")) return;
  if (t.closest("[data-finding-row]")) return;
  const row = t.closest<HTMLElement>("[data-row-link]");
  if (row) {
    const href = row.getAttribute("data-row-link");
    if (href) window.location.href = href;
  }
});

// Workspace boot: facets, search, selection state, resizable panes.
initFindingFilters();
initResizable();

// HTMX swaps the right pane on row click; afterSwap re-applies the
// row-selected highlight to the row that was clicked (htmx clears the
// previous selection's class in the markup, but the new row's selected
// state lives in the URL, not the swap response).
document.addEventListener("htmx:afterSwap", (e) => {
  const target = (e as CustomEvent).detail?.target as HTMLElement | undefined;
  if (target && target.id === "detail-pane") {
    refreshSelection();
  }
  // Any swap may bring in fresh review / outcome forms with rich
  // selectbox triggers (severity, status). Re-seed the displays so
  // pre-selected items render their chip rather than the empty
  // placeholder.
  if (target) refreshRichSelectDisplays(target);
});

// Browser back/forward in the workspace updates the URL but doesn't
// re-render server-side; the user expects the list selection to
// follow. (htmx:historyRestore covers the htmx-tracked case; popstate
// covers everything else.)
window.addEventListener("popstate", () => refreshSelection());
