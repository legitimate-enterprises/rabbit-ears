import { errorLine, h, showError, sub } from "./dom";

export const header = (title: string, caption: string, ...actions: HTMLElement[]) =>
  h("div.head", h("h1", title), sub(caption), h("div.actions", ...actions));

export const card = (title: string, ...kids: (Node | string | null)[]) =>
  h("section.card", { "aria-label": title }, h("h2", title), ...kids);

export const bar = (fraction: number) =>
  h(
    "div.bar",
    {
      role: "progressbar",
      "aria-valuemin": 0,
      "aria-valuemax": 100,
      "aria-valuenow": Math.round(fraction * 100),
    },
    h("i", { style: `width:${Math.round(Math.min(1, Math.max(0, fraction)) * 100)}%` }),
  );

export function table(cols: string[], rows: (Node | string | null)[][], empty: string) {
  if (!rows.length) return h("p.sub", empty);
  return h(
    "table",
    h("thead", h("tr", ...cols.map((c) => h("th", c)))),
    h(
      "tbody",
      ...rows.map((r) =>
        h("tr", ...r.map((c, i) => h(i === r.length - 1 && cols[i] === "" ? "td.act" : "td", c))),
      ),
    ),
  );
}

export const copy = (text: string) => navigator.clipboard?.writeText(text);

export const gauge = (
  name: string,
  value: string,
  unit: string,
  fraction: number,
  caption?: string,
) =>
  h(
    "section.card.gauge",
    { "aria-label": name },
    h("h2", name),
    h("div.num", value, h("small", unit)),
    bar(fraction),
    caption ? sub(caption) : null,
  );

export const setting = (label: string, caption: string | null, ...controls: (Node | null)[]) =>
  h("div.set", h("div.l", h("b", label), caption ? sub(caption) : null), ...controls);

/** A dialog form's Cancel and its submit button, labeled. */
export const formEnd = (close: () => void, label: string) =>
  h(
    "div.end",
    h("button.ib", { type: "button", onclick: close }, "Cancel"),
    h("button.ib.p", { type: "submit" }, label),
  );

/** A switch that saves each flip, and on a refusal flips back and shows why in err. */
export function saveSwitch(
  label: string,
  checked: boolean,
  err: HTMLElement,
  save: (on: boolean) => Promise<unknown>,
) {
  const sw = h("input", {
    type: "checkbox",
    role: "switch",
    "aria-label": label,
    checked,
    onchange: async () => {
      showError(err);
      try {
        await save(sw.checked);
      } catch (x) {
        sw.checked = !sw.checked;
        showError(err, (x as Error).message);
      }
    },
  }) as HTMLInputElement;
  return sw;
}

/**
 * A destructive button that asks in the page before acting, never with
 * window.confirm. run's message, if any, shows beside the button afterwards.
 */
export function confirming(label: string, question: string, run: () => Promise<string | void>) {
  const wrap = h("span.confirm");
  const err = errorLine();
  function idle(note?: string) {
    wrap.replaceChildren(
      note ? sub(note) : "",
      h("button.ib.danger", { type: "button", onclick: () => ask() }, label),
      err,
    );
  }
  function ask() {
    showError(err);
    wrap.replaceChildren(
      sub(question),
      h("button.ib", { type: "button", onclick: () => idle() }, "Cancel"),
      h("button.ib.danger", { type: "button", onclick: go }, label),
    );
  }
  async function go() {
    try {
      idle((await run()) ?? undefined);
    } catch (x) {
      idle();
      showError(err, (x as Error).message);
    }
  }
  idle();
  return wrap;
}
