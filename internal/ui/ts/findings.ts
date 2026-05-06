// Three-pane workspace: faceted filters + selection.
//
// All state is read from the DOM; nothing is persisted. Filtering hides
// rows via the `hidden` attribute (rather than rebuilding the list)
// so HTMX-targeted clicks keep working on the live nodes.

type FacetName = "severity" | "label";

const ROW_SELECTOR = "[data-finding-row]";
const FILTER_PANE = "[data-finding-filters]";
const SEARCH_INPUT = "[data-finding-search]";
const FILE_FILTER_INPUT = "[data-finding-file-filter]";
const FACET_BOX = "[data-finding-facet]";
const STALE_TOGGLE = "[data-finding-stale-toggle]";
const COUNT_EL = "[data-finding-count]";
const EMPTY_EL = "[data-finding-empty]";
const CLEAR_BTN = "[data-finding-clear]";

let scrolledToSelection = false;

export function initFindingFilters() {
  const pane = document.querySelector<HTMLElement>(FILTER_PANE);
  if (!pane) return;

  const search = pane.querySelector<HTMLInputElement>(SEARCH_INPUT);
  const fileFilter = pane.querySelector<HTMLInputElement>(FILE_FILTER_INPUT);
  const facetBoxes = Array.from(
    pane.querySelectorAll<HTMLInputElement>(FACET_BOX),
  );
  const staleToggle = pane.querySelector<HTMLInputElement>(STALE_TOGGLE);
  const clearBtn = pane.querySelector<HTMLButtonElement>(CLEAR_BTN);

  const apply = () => applyFilters();

  search?.addEventListener("input", apply);
  fileFilter?.addEventListener("input", apply);
  facetBoxes.forEach((box) => box.addEventListener("change", apply));
  staleToggle?.addEventListener("change", apply);
  clearBtn?.addEventListener("click", () => {
    if (search) search.value = "";
    if (fileFilter) fileFilter.value = "";
    facetBoxes.forEach((b) => (b.checked = false));
    if (staleToggle) staleToggle.checked = false;
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

  const fileInput = document.querySelector<HTMLInputElement>(FILE_FILTER_INPUT);
  const fileMatcher = compileFileMatcher((fileInput?.value || "").trim());

  const active = collectActiveFacets();
  const hideStale =
    document.querySelector<HTMLInputElement>(STALE_TOGGLE)?.checked === true;

  const rows = document.querySelectorAll<HTMLElement>(ROW_SELECTOR);
  let visible = 0;
  rows.forEach((row) => {
    const show = matches(row, query, active, hideStale, fileMatcher);
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

// compileFileMatcher turns the file-filter input into a predicate.
// Pattern syntax:
//   - `*` matches any sequence of characters (including `/` and empty).
//   - All other characters match literally.
//   - The match is left-anchored: `internal/anchor` is treated as
//     `internal/anchor*`. Equivalently, an implicit trailing `*` is
//     appended unless one is already there.
//   - Comparison is case-insensitive (consistent with the search input).
// Empty pattern matches everything.
function compileFileMatcher(pattern: string): (file: string) => boolean {
  if (!pattern) return () => true;
  // Escape regex metacharacters except `*`, then convert `*` → `.*`.
  const escaped = pattern.replace(/[.+?^${}()|[\]\\]/g, "\\$&").replace(/\*/g, ".*");
  // Implicit trailing `*` so prefix patterns "just work" without the user
  // remembering to add one. If the user already ended with `*`, that's
  // already `.*` — adding another `.*` is a no-op.
  const re = new RegExp("^" + escaped + ".*$", "i");
  return (file) => re.test(file);
}

function matches(
  row: HTMLElement,
  query: string,
  active: Record<FacetName, Set<string>>,
  hideStale: boolean,
  fileMatcher: (file: string) => boolean,
): boolean {
  if (query) {
    const haystack = (row.getAttribute("data-search") || "").toLowerCase();
    if (!haystack.includes(query)) return false;
  }
  // File filter is checked against the row's literal file path
  // (data-file), not the broader data-search haystack — `*.go` should
  // match by extension, not happen to find ".go" inside a description.
  if (!fileMatcher(row.getAttribute("data-file") || "")) return false;
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
  if (hideStale && row.getAttribute("data-anchor") === "stale") {
    return false;
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
