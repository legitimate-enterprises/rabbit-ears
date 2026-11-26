// Tiny DOM builder: h("button.ib.p", { onclick }, "Save"). Text children are
// set as text, never HTML.
type Child = Node | string | false | null | undefined;
type Props = Record<string, unknown>;

export function h(spec: string, props: Props | Child = {}, ...kids: Child[]): HTMLElement {
  const [tag, ...classes] = spec.split(".");
  const el = document.createElement(tag || "div");
  if (classes.length) el.className = classes.join(" ");
  if (props instanceof Node || typeof props === "string" || !props) kids.unshift(props as Child);
  else
    for (const [k, v] of Object.entries(props)) {
      if (v === undefined || v === false) continue;
      if (k.startsWith("on")) el.addEventListener(k.slice(2), v as EventListener);
      else if (k in el && k !== "list") (el as unknown as Props)[k] = v;
      else el.setAttribute(k, v === true ? "" : String(v));
    }
  for (const k of kids) if (k) el.append(k);
  return el;
}

export const sub = (text: string) => h("span.sub", text);
export const tag = (text: string, kind = "") => h(`span.tag${kind ? "." + kind : ""}`, text);

export function field(label: string, input: HTMLInputElement | HTMLSelectElement, hint?: string) {
  const id = `f-${Math.random().toString(36).slice(2, 8)}`;
  input.id = id;
  return h(
    "div.field",
    h("label", { htmlFor: id }, label),
    input,
    hint ? h("span.sub.hint", hint) : null,
  );
}

// A checkbox keeps its own size; only fields take the field's look.
export const input = (type: string, props: Props = {}) =>
  h(type === "checkbox" ? "input" : "input.in", { type, ...props }) as HTMLInputElement;

export function dialog(title: string, ...body: Child[]) {
  const titleId = `d-${Math.random().toString(36).slice(2, 8)}`;
  const scrim = h("div.scrim");
  const box = h(
    "div.dialog",
    { role: "dialog", "aria-modal": "true", "aria-labelledby": titleId },
    h("h1", { id: titleId }, title),
    ...body,
  );
  scrim.append(box);
  const close = () => scrim.remove();
  scrim.addEventListener("keydown", (e) => e.key === "Escape" && close());
  document.body.append(scrim);
  (box.querySelector("input, button") as HTMLElement | null)?.focus();
  return { box, close };
}

export function errorLine(): HTMLElement {
  return h("p.err", { role: "alert", hidden: true });
}
export function showError(el: HTMLElement, message?: string) {
  el.textContent = message ?? "";
  el.hidden = !message;
}
