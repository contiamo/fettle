// Three-pane workspace: faceted filters + selection.
//
// All state is read from the DOM; nothing is persisted. Filtering hides
// rows via the `hidden` attribute (rather than rebuilding the list)
// so HTMX-targeted clicks keep working on the live nodes.

type FacetName = "severity" | "label" | "outcome";

const ROW_SELECTOR = "[data-finding-row]";
const FILTER_PANE = "[data-finding-filters]";
const LIST_PANE = "[data-finding-list-pane]";
const SEARCH_INPUT = "[data-finding-search]";
const FILE_FILTER_INPUT = "[data-finding-file-filter]";
const FACET_BOX = "[data-finding-facet]";
const STALE_TOGGLE = "[data-finding-stale-toggle]";
const COUNT_EL = "[data-finding-count]";
const EMPTY_EL = "[data-finding-empty]";
const CLEAR_BTN = "[data-finding-clear]";

const BULK_TOGGLE = "[data-bulk-toggle]";
const BULK_CONTROLS = "[data-bulk-controls]";
const BULK_SUMMARY_ROW = "[data-bulk-summary-row]";
const BULK_SELECT_ALL = "[data-bulk-select-all]";
const BULK_COUNT = "[data-bulk-count]";
const BULK_REVIEW_BUTTON_WRAP = "[data-bulk-review-button-wrap]";
const BULK_REVIEW_TOGGLE = "[data-bulk-review-toggle]";
const BULK_REVIEW_FORM = "[data-bulk-review-form]";
const BULK_CANCEL = "[data-bulk-cancel]";
const BULK_CANCEL_MODE = "[data-bulk-cancel-mode]";
const BULK_SUBMIT_LABEL = "[data-bulk-submit-label]";
const BULK_ROW_CHECKBOX = "[data-bulk-row-checkbox]";

// Bulk-selection model — single explicit allow-list.
//
// Entering bulk mode starts the selection empty. The reviewer ticks
// rows to add to it (or clicks the header checkbox to flip between
// "all visible" and "none"). The "All" label shown when nothing is
// selected is informational — it identifies the implicit pool the
// reviewer is picking from, not a pre-applied state. Review only
// surfaces when the selection is non-empty.
//
// Filter changes keep bulk-mode on but reset the selection, since
// the scope just shifted; carrying a stale set into a new filter
// would silently change the meaning of the next action.
//
// Single mode (no positive/negative duality) is enough here because
// findings are loaded entirely client-side — there's no "rows you
// haven't loaded yet" case where the pattern's matching-with-exclude
// earned its keep.
type BulkState =
  | { kind: "off" }
  | { kind: "on"; selected: Set<string> };

let bulkState: BulkState = { kind: "off" };
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

  const apply = () => {
    applyFilters();
    // Filter scope changed — selection's meaning depends on scope
    // (especially in matching mode), so resetting is the safe move.
    // No-op when bulk mode is off.
    resetBulkOnFilterChange();
  };

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

  initBulkSelection();
  initEditToggles();
  initRichSelect();
  refreshRichSelectDisplays();

  // Reflect the initial selection (server-rendered) and scroll the
  // active row into view once. Subsequent selections happen through
  // HTMX swaps; refreshSelection runs again then.
  refreshSelection();
  scrollSelectedIntoView();
}

// initEditToggles wires the pencil-edit / cancel-edit buttons that
// gate review-form fields (labels, severity) behind a "no change"
// preview. Behaviour is scope-local: each editable field is wrapped
// in a [data-edit-field] container, and clicks on its show/revert
// buttons only toggle elements inside that container — so the
// per-finding review form and the bulk review form coexisting on
// one page never step on each other.
//
// Toggling "show" enables the underlying input so it submits with
// the form; "revert" disables it again so an unchanged field stays
// out of the post body (server interprets the absence as nil — "no
// change to this axis"). The disabled-by-default state is rendered
// server-side, so initial page load always submits no fields.
//
// Event delegation lives on document so the wiring survives the
// HTMX swap that replaces #detail-pane content (and with it the
// per-finding review form) on every row click. Re-binding listeners
// after every swap would also work but is more bookkeeping.
function initEditToggles() {
  document.addEventListener("click", (ev) => {
    const t = ev.target as HTMLElement | null;
    if (!t) return;
    const show = t.closest<HTMLElement>("[data-edit-show]");
    if (show) {
      setEditMode(show, true);
      return;
    }
    const revert = t.closest<HTMLElement>("[data-edit-revert]");
    if (revert) setEditMode(revert, false);
  });
}

// initRichSelect wires templui selectbox triggers that opted out of
// the stock textContent-only Value display in favour of our own
// rich [data-rich-select-display] span. On every selectbox-item
// click anywhere in the document, we clone the selected item's
// .select-item-text innerHTML into the corresponding trigger
// display, so SeverityPill / OutcomePill chips render in the
// trigger exactly as they do in the dropdown menu.
//
// Templui's own click handler runs first (selecting the item,
// updating the hidden input, closing the popover); ours runs
// afterwards via document-level event delegation.
function initRichSelect() {
  document.addEventListener("click", (ev) => {
    const item = (ev.target as HTMLElement | null)?.closest<HTMLElement>(
      ".select-item[data-tui-selectbox-value]",
    );
    if (!item) return;
    const container = item.closest<HTMLElement>(".select-container");
    if (!container) return;
    const display = container.querySelector<HTMLElement>(
      ".select-trigger [data-rich-select-display]",
    );
    if (!display) return;
    const text = item.querySelector<HTMLElement>(".select-item-text");
    if (text) display.innerHTML = text.innerHTML;
  });
}

// refreshRichSelectDisplays seeds each rich display with the HTML
// of its currently-selected item, if any. Called on initial page
// load and after every htmx:afterSwap so freshly-rendered review /
// outcome forms reflect their pre-selected values.
//
// When nothing is selected, the placeholder span the templ emits
// stays in place — rendering as muted "Pick severity" / "Pick
// status" text until the user actually picks something.
export function refreshRichSelectDisplays(root: ParentNode = document) {
  root.querySelectorAll<HTMLElement>("[data-rich-select-display]").forEach((display) => {
    const container = display.closest<HTMLElement>(".select-container");
    if (!container) return;
    const text = container.querySelector<HTMLElement>(
      ".select-item[data-tui-selectbox-selected='true'] .select-item-text",
    );
    if (text) display.innerHTML = text.innerHTML;
  });
}

function setEditMode(button: HTMLElement, editing: boolean) {
  const field = button.closest<HTMLElement>("[data-edit-field]");
  if (!field) return;
  const preview = field.querySelector<HTMLElement>("[data-edit-preview]");
  const editor = field.querySelector<HTMLElement>("[data-edit-editor]");
  if (preview) preview.hidden = editing;
  if (!editor) return;
  editor.hidden = !editing;

  // Toggle every form-submitting control's disabled state — that's
  // how "didn't engage editor" stays out of the post body. The
  // selectbox component renders both a visible trigger button and
  // a hidden <input type="hidden"> that actually carries the value;
  // both need flipping. Plain input/textarea fields have just the
  // single visible control.
  const controls = editor.querySelectorAll<
    HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement | HTMLButtonElement
  >("input, select, textarea, button.select-trigger");
  controls.forEach((c) => {
    c.disabled = !editing;
  });

  if (editing) {
    const focusable = editor.querySelector<HTMLElement>(
      "input:not([type=hidden]):not([disabled]), select:not([disabled]), textarea:not([disabled]), button.select-trigger:not([disabled])",
    );
    focusable?.focus();
  }
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
    outcome: new Set(),
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
  if (active.outcome.size > 0) {
    // Empty data-outcome (no recorded outcome) matches the rail's
    // "no outcome" facet checkbox, which uses value="".
    const outcome = row.getAttribute("data-outcome") || "";
    if (!active.outcome.has(outcome)) return false;
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

// ---------------------------------------------------------------------------
// Bulk selection
// ---------------------------------------------------------------------------

function initBulkSelection() {
  const toggle = document.querySelector<HTMLButtonElement>(BULK_TOGGLE);
  if (!toggle) return;

  toggle.addEventListener("click", () => {
    if (bulkState.kind === "off") {
      bulkState = { kind: "on", selected: new Set() };
    } else {
      bulkState = { kind: "off" };
    }
    syncBulkUI();
  });

  document
    .querySelector<HTMLInputElement>(BULK_SELECT_ALL)
    ?.addEventListener("change", () => {
      if (bulkState.kind !== "on") return;
      // Bidirectional shortcut: when nothing's selected (or some-but-
      // not-all), select every visible row; otherwise clear the
      // selection. Saves the reviewer ticking 60+ checkboxes by hand.
      const total = countVisibleRows();
      if (bulkState.selected.size < total) {
        const all = new Set<string>();
        document.querySelectorAll<HTMLElement>(ROW_SELECTOR).forEach((row) => {
          if (row.hidden) return;
          const id = row.getAttribute("data-finding-id");
          if (id) all.add(id);
        });
        bulkState = { kind: "on", selected: all };
      } else {
        bulkState = { kind: "on", selected: new Set() };
      }
      syncBulkUI();
    });

  document.querySelectorAll<HTMLInputElement>(BULK_ROW_CHECKBOX).forEach((cb) => {
    cb.addEventListener("change", () => onRowCheckboxChange(cb));
  });

  document
    .querySelector<HTMLButtonElement>(BULK_CANCEL_MODE)
    ?.addEventListener("click", () => {
      bulkState = { kind: "off" };
      syncBulkUI();
    });

  document
    .querySelector<HTMLButtonElement>(BULK_REVIEW_TOGGLE)
    ?.addEventListener("click", () => {
      const form = document.querySelector<HTMLFormElement>(BULK_REVIEW_FORM);
      if (form) form.hidden = false;
    });

  document.querySelector<HTMLButtonElement>(BULK_CANCEL)?.addEventListener("click", () => {
    const form = document.querySelector<HTMLFormElement>(BULK_REVIEW_FORM);
    if (form) form.hidden = true;
  });

  document
    .querySelector<HTMLFormElement>(BULK_REVIEW_FORM)
    ?.addEventListener("submit", onBulkFormSubmit);
}

function onRowCheckboxChange(cb: HTMLInputElement) {
  if (bulkState.kind !== "on") {
    cb.checked = false;
    return;
  }
  const id = cb.value;
  if (cb.checked) bulkState.selected.add(id);
  else bulkState.selected.delete(id);
  syncBulkUI();
}

function onBulkFormSubmit(ev: SubmitEvent) {
  const form = ev.currentTarget as HTMLFormElement;
  // Strip any prior finding_ids hidden inputs (in case the form was
  // shown, dismissed, then submitted twice in one page life).
  form
    .querySelectorAll<HTMLInputElement>('input[name="finding_ids"]')
    .forEach((i) => i.remove());
  const ids = resolveSelectionIds();
  if (ids.length === 0) {
    ev.preventDefault();
    return;
  }
  for (const id of ids) {
    const hidden = document.createElement("input");
    hidden.type = "hidden";
    hidden.name = "finding_ids";
    hidden.value = id;
    form.appendChild(hidden);
  }
}

// resolveSelectionIds turns the bulk state into a concrete id list
// at submit time. We intersect with currently-visible rows so a
// selection that was made before the user changed the filter scope
// can't accidentally re-include rows that no longer match — though
// resetBulkOnFilterChange already empties the set on filter change,
// the intersect is belt-and-suspenders against any future code that
// shifts visibility without going through the filter handlers.
function resolveSelectionIds(): string[] {
  if (bulkState.kind !== "on") return [];
  const visibleIds = new Set<string>();
  document.querySelectorAll<HTMLElement>(ROW_SELECTOR).forEach((row) => {
    if (row.hidden) return;
    const id = row.getAttribute("data-finding-id");
    if (id) visibleIds.add(id);
  });
  return [...bulkState.selected].filter((id) => visibleIds.has(id));
}

function syncBulkUI() {
  const pane = document.querySelector<HTMLElement>(LIST_PANE);
  const controls = document.querySelector<HTMLElement>(BULK_CONTROLS);
  const summaryRow = document.querySelector<HTMLElement>(BULK_SUMMARY_ROW);
  const toggle = document.querySelector<HTMLButtonElement>(BULK_TOGGLE);
  const selectAll = document.querySelector<HTMLInputElement>(BULK_SELECT_ALL);
  const countEl = document.querySelector<HTMLElement>(BULK_COUNT);
  const reviewWrap = document.querySelector<HTMLElement>(BULK_REVIEW_BUTTON_WRAP);
  const submitLabel = document.querySelector<HTMLElement>(BULK_SUBMIT_LABEL);
  const reviewForm = document.querySelector<HTMLFormElement>(BULK_REVIEW_FORM);

  const on = bulkState.kind === "on";
  pane?.classList.toggle("bulk-on", on);
  if (controls) controls.hidden = !on;
  if (summaryRow) summaryRow.hidden = on; // hide the date/count line when bulk is up
  toggle?.setAttribute("aria-pressed", on ? "true" : "false");

  if (bulkState.kind !== "on") {
    if (reviewForm) reviewForm.hidden = true;
    if (reviewWrap) reviewWrap.hidden = true;
    document
      .querySelectorAll<HTMLInputElement>(BULK_ROW_CHECKBOX)
      .forEach((cb) => {
        cb.checked = false;
        cb.indeterminate = false;
      });
    if (selectAll) {
      selectAll.checked = false;
      selectAll.indeterminate = false;
    }
    return;
  }
  // Hoist the set into a local so subsequent narrowing survives
  // across arrow-function callbacks (TS doesn't preserve discriminated-
  // union narrowing through closure boundaries).
  const selected = bulkState.selected;
  const total = countVisibleRows();

  if (countEl) countEl.textContent = formatBulkCount(selected.size, total);
  if (reviewWrap) reviewWrap.hidden = selected.size === 0;
  if (submitLabel) {
    submitLabel.textContent =
      selected.size === 1 ? "Save 1 review" : `Save ${selected.size} reviews`;
  }

  document
    .querySelectorAll<HTMLInputElement>(BULK_ROW_CHECKBOX)
    .forEach((cb) => {
      cb.checked = selected.has(cb.value);
    });

  if (selectAll) {
    if (selected.size === 0) {
      selectAll.checked = false;
      selectAll.indeterminate = false;
    } else if (selected.size === total) {
      selectAll.checked = true;
      selectAll.indeterminate = false;
    } else {
      selectAll.checked = false;
      selectAll.indeterminate = true;
    }
  }
}

// formatBulkCount labels the empty state "All" — informational, the
// pool you're picking from — and switches to "X of Y" once anything
// is selected, with "All selected" as the upper bound to differentiate
// "nothing picked" (also reads "All") from "everything picked".
function formatBulkCount(selected: number, total: number): string {
  if (total === 0) return "no findings";
  if (selected === 0) return "All";
  if (selected === total) return "All selected";
  return `${selected} of ${total}`;
}

function countVisibleRows(): number {
  let n = 0;
  document.querySelectorAll<HTMLElement>(ROW_SELECTOR).forEach((row) => {
    if (!row.hidden) n++;
  });
  return n;
}

function resetBulkOnFilterChange() {
  if (bulkState.kind !== "on") return;
  // Filter scope shifted; the prior selection's meaning depends on
  // what was visible at the time. Clearing keeps the model honest —
  // the reviewer reselects under the new scope.
  bulkState = { kind: "on", selected: new Set() };
  syncBulkUI();
}
