/**
 * Get the target of an event as an Element.
 * e.target can be a Text node (which has no .closest()), so we fall back
 * to parentElement when it isn't an Element.
 */
export function targetEl(e: Event): Element | null {
  const t = e.target;
  if (t instanceof Element) return t;
  if (t instanceof Node) return t.parentElement;
  return null;
}
