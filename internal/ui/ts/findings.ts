// Three-pane workspace: faceted filters + selection.
//
// All state is read from the DOM; nothing is persisted. Filtering hides
// rows via the `hidden` attribute (rather than rebuilding the list)
// so HTMX-targeted clicks keep working on the live nodes.

type FacetName = "severity" | "label";

const ROW_SELECTOR = "[data-finding-row]";
const FILTER_PANE = "[data-finding-filters]";
const SEARCH_INPUT = "[data-finding-search]";
const FACET_BOX = "[data-finding-facet]";
const COUNT_EL = "[data-finding-count]";
const EMPTY_EL = "[data-finding-empty]";
const CLEAR_BTN = "[data-finding-clear]";

let scrolledToSelection = false;

export function initFindingFilters() {
  const pane = document.querySelector<HTMLElement>(FILTER_PANE);
  if (!pane) return;

  const search = pane.querySelector<HTMLInputElement>(SEARCH_INPUT);
  const facetBoxes = Array.from(
    pane.querySelectorAll<HTMLInputElement>(FACET_BOX),
  );
  const clearBtn = pane.querySelector<HTMLButtonElement>(CLEAR_BTN);

  const apply = () => applyFilters();

  search?.addEventListener("input", apply);
  facetBoxes.forEach((box) => box.addEventListener("change", apply));
  clearBtn?.addEventListener("click", () => {
    if (search) search.value = "";
    facetBoxes.forEach((b) => (b.checked = false));
    apply();
  });

  // Reflect the initial selection (server-rendered) and scroll the
  // active row into view once. Subsequent selections happen through
  // HTMX swaps; refreshSelection runs again then.
  refreshSelection();
  scrollSelectedIntoView();
}

function applyFilters() {
  const search = document.querySelector<HTMLInputElement>(SEARCH_INPUT);
  const query = (search?.value || "").trim().toLowerCase();

  const active = collectActiveFacets();

  const rows = document.querySelectorAll<HTMLElement>(ROW_SELECTOR);
  let visible = 0;
  rows.forEach((row) => {
    const show = matches(row, query, active);
    row.hidden = !show;
    if (show) visible++;
  });

  // Update list-pane footer count + empty placeholder.
  const count = document.querySelector<HTMLElement>(COUNT_EL);
  if (count) {
    count.textContent =
      visible === rows.length
        ? `${rows.length} ${rows.length === 1 ? "finding" : "findings"}`
        : `${visible} of ${rows.length}`;
  }
  const empty = document.querySelector<HTMLElement>(EMPTY_EL);
  if (empty) empty.classList.toggle("hidden", visible !== 0);
}

function collectActiveFacets(): Record<FacetName, Set<string>> {
  const out: Record<FacetName, Set<string>> = {
    severity: new Set(),
    label: new Set(),
  };
  document
    .querySelectorAll<HTMLInputElement>(`${FACET_BOX}:checked`)
    .forEach((box) => {
      const facet = box.getAttribute("data-finding-facet") as FacetName | null;
      if (!facet) return;
      out[facet].add(box.value);
    });
  return out;
}

function matches(
  row: HTMLElement,
  query: string,
  active: Record<FacetName, Set<string>>,
): boolean {
  if (query) {
    const haystack = (row.getAttribute("data-search") || "").toLowerCase();
    if (!haystack.includes(query)) return false;
  }
  if (active.severity.size > 0) {
    const sev = row.getAttribute("data-severity") || "";
    if (!active.severity.has(sev)) return false;
  }
  if (active.label.size > 0) {
    const labels = (row.getAttribute("data-labels") || "")
      .split(" ")
      .filter(Boolean);
    // Within a facet group we OR (any selected label matches);
    // standard faceted-search behaviour.
    let any = false;
    for (const l of labels) {
      if (active.label.has(l)) {
        any = true;
        break;
      }
    }
    if (!any) return false;
  }
  return true;
}

// refreshSelection paints the currently-active row (per ?focus= or the
// first row by fallback) and clears any stale selection class.
export function refreshSelection() {
  const focus = focusFromUrl();
  const rows = document.querySelectorAll<HTMLElement>(ROW_SELECTOR);
  rows.forEach((row) => {
    const id = row.getAttribute("data-finding-id");
    const active = id !== null && id === focus;
    row.classList.toggle("row-selected", active);
    row.setAttribute("aria-selected", active ? "true" : "false");
  });
}

function focusFromUrl(): string | null {
  const params = new URLSearchParams(window.location.search);
  const explicit = params.get("focus");
  if (explicit) return explicit;
  // No ?focus= → server defaults to the first finding; mirror that
  // here so the highlight matches the rendered detail pane.
  const first = document.querySelector<HTMLElement>(ROW_SELECTOR);
  return first ? first.getAttribute("data-finding-id") : null;
}

function scrollSelectedIntoView() {
  if (scrolledToSelection) return;
  const selected = document.querySelector<HTMLElement>(
    `${ROW_SELECTOR}.row-selected`,
  );
  if (!selected) return;
  // `nearest` keeps the page from jumping when the selection is
  // already on screen — common case on first paint of the workspace.
  selected.scrollIntoView({ block: "nearest" });
  scrolledToSelection = true;
}
