// Resizable list/detail split for the workspace.
//
// Patterns lifted from react-resizable-panels (rrp) + shadcn:
//   * Storage in **percent** of the resizable container, not pixels —
//     so the layout scales when the browser resizes.
//   * Pointer capture during drag (works across iframes / window
//     edges; lets the cursor leave the handle without breaking).
//   * `touch-action: none` on the handle (rrp #662) so a touch-drag
//     doesn't trigger page scroll.
//   * ARIA: role="separator" + aria-orientation + aria-valuemin/max/now,
//     so screen readers announce the current split.
//   * Keyboard: Arrow ±5%, Home/End jump to the min/max bound (also
//     rrp's defaults).
//   * Save on pointer-up (or after a keyboard adjustment), not on
//     every move — avoids hammering localStorage.
//
// What we deliberately drop vs rrp: nested groups, conditional panel
// mounting, F6 separator cycling, custom storage adapters, debounced
// saves. We have one separator between two known panels — keeping
// the controller small is the whole point.

const STORAGE_KEY = "fettle:workspace-split";
const DEFAULT_PERCENT = 38;
const STEP_PERCENT = 5;
// Hard pixel floors. The container's available width minus these two
// values bounds how far the user can drag the handle.
const MIN_LIST_PX = 280;
const MIN_DETAIL_PX = 380;

export function initResizable() {
  const handle = document.querySelector<HTMLElement>("[data-resize-handle]");
  const list = document.querySelector<HTMLElement>("[data-resize-list]");
  const container = document.querySelector<HTMLElement>(
    "[data-resize-container]",
  );
  if (!handle || !list || !container) return;

  // Restore saved % (clamped to current bounds — the saved value may
  // have been written at a wider viewport than the current one).
  const saved = readSaved();
  applyPercent(list, container, handle, saved ?? DEFAULT_PERCENT);

  let dragging = false;
  let containerRect: DOMRect | null = null;
  let handleWidth = 0;

  handle.addEventListener("pointerdown", (e) => {
    // Mouse: only left button starts a drag.
    if (e.pointerType === "mouse" && e.button !== 0) return;
    dragging = true;
    containerRect = container.getBoundingClientRect();
    handleWidth = handle.getBoundingClientRect().width;
    handle.setPointerCapture(e.pointerId);
    handle.classList.add("is-dragging");
    document.body.style.cursor = "col-resize";
    document.body.style.userSelect = "none";
    e.preventDefault();
  });

  handle.addEventListener("pointermove", (e) => {
    if (!dragging || !containerRect) return;
    // Position the handle relative to the container; subtract the
    // handle's own width so the list pane's width is reported as the
    // mouse position minus the handle (matches what the user sees).
    const x = e.clientX - containerRect.left;
    const usable = containerRect.width - handleWidth;
    if (usable <= 0) return;
    const percent = (x / usable) * 100;
    applyPercent(list, container, handle, percent);
  });

  const endDrag = (e: PointerEvent) => {
    if (!dragging) return;
    dragging = false;
    containerRect = null;
    if (handle.hasPointerCapture(e.pointerId)) {
      handle.releasePointerCapture(e.pointerId);
    }
    handle.classList.remove("is-dragging");
    document.body.style.cursor = "";
    document.body.style.userSelect = "";
    persist(currentPercent(list));
  };
  handle.addEventListener("pointerup", endDrag);
  handle.addEventListener("pointercancel", endDrag);

  // Double-click resets to the default split. shadcn/rrp's reset
  // affordance — handy when the user wants out of an awkward layout.
  handle.addEventListener("dblclick", () => {
    applyPercent(list, container, handle, DEFAULT_PERCENT);
    persist(DEFAULT_PERCENT);
  });

  handle.addEventListener("keydown", (e) => {
    const cur = currentPercent(list);
    let next = cur;
    switch (e.key) {
      case "ArrowLeft":
        next = cur - STEP_PERCENT;
        break;
      case "ArrowRight":
        next = cur + STEP_PERCENT;
        break;
      case "Home":
        next = 0; // clamped to the pixel floor by applyPercent
        break;
      case "End":
        next = 100; // clamped to the pixel floor by applyPercent
        break;
      default:
        return;
    }
    e.preventDefault();
    applyPercent(list, container, handle, next);
    persist(currentPercent(list));
  });

  // The min/max ARIA bounds depend on container width — recompute on
  // resize so screen readers + the visual constraint stay in sync.
  window.addEventListener("resize", () => {
    applyPercent(list, container, handle, currentPercent(list));
  });
}

function applyPercent(
  list: HTMLElement,
  container: HTMLElement,
  handle: HTMLElement,
  pct: number,
) {
  const { min, max } = boundsPercent(container, handle);
  const clamped = Math.max(min, Math.min(max, pct));
  list.style.flexBasis = `${clamped.toFixed(2)}%`;
  handle.setAttribute("aria-valuenow", `${Math.round(clamped)}`);
  handle.setAttribute("aria-valuemin", `${Math.round(min)}`);
  handle.setAttribute("aria-valuemax", `${Math.round(max)}`);
}

function boundsPercent(
  container: HTMLElement,
  handle: HTMLElement,
): { min: number; max: number } {
  const rect = container.getBoundingClientRect();
  const handleW = handle.getBoundingClientRect().width;
  const usable = rect.width - handleW;
  if (usable <= 0) return { min: 0, max: 100 };
  let min = (MIN_LIST_PX / usable) * 100;
  let max = ((usable - MIN_DETAIL_PX) / usable) * 100;
  // Degenerate viewports: if there isn't even enough room for both
  // min sizes, collapse the bounds rather than producing min > max.
  if (min > max) {
    const mid = (min + max) / 2;
    min = mid;
    max = mid;
  }
  return { min, max };
}

function currentPercent(list: HTMLElement): number {
  const raw = list.style.flexBasis;
  if (raw.endsWith("%")) return parseFloat(raw);
  return DEFAULT_PERCENT;
}

function readSaved(): number | null {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (raw === null) return null;
    const n = parseFloat(raw);
    return Number.isFinite(n) ? n : null;
  } catch {
    // localStorage may throw in private mode or with quota issues.
    // Falling back to the default is the right behaviour here.
    return null;
  }
}

function persist(pct: number) {
  try {
    localStorage.setItem(STORAGE_KEY, pct.toFixed(2));
  } catch {
    // Same rationale as readSaved: silently ignore.
  }
}
