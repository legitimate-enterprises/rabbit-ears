// Keyboard shortcuts: one table for the keys the app answers and the sheet
// that lists them. A Mac browser takes Cmd where every other takes Ctrl.
import { h } from "../admin/dom";

export type Command =
  | "search"
  | "back"
  | "forward"
  | "home"
  | "movies"
  | "tv"
  | "music"
  | "youtube"
  | "stations"
  | "help";

const isMac = () => /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);

/** Where each of Mod-1 to Mod-6 goes. */
export const SECTIONS: [Command, string][] = [
  ["home", "#/"],
  ["movies", "#/movies"],
  ["tv", "#/tv"],
  ["music", "#/music"],
  ["youtube", "#/youtube"],
  ["stations", "#/stations"],
];

const typing = (t: EventTarget | null) =>
  t instanceof HTMLElement &&
  (t.isContentEditable ||
    t instanceof HTMLTextAreaElement ||
    t instanceof HTMLSelectElement ||
    (t instanceof HTMLInputElement && !["checkbox", "radio", "range", "button"].includes(t.type)));

/** The app-wide command a key press asks for; single keys never while typing. */
export function command(e: KeyboardEvent, mac = isMac()): Command | undefined {
  if (e.altKey) return;
  const mod = mac ? e.metaKey && !e.ctrlKey : e.ctrlKey && !e.metaKey;
  if (mod && !e.shiftKey) {
    if (e.key === "f") return "search";
    if (e.key === "[") return "back";
    if (e.key === "]") return "forward";
    const n = Number(e.key);
    if (n >= 1 && n <= SECTIONS.length) return SECTIONS[n - 1][0];
    return;
  }
  if (!e.metaKey && !e.ctrlKey && e.key === "?" && !typing(e.target)) return "help";
}

/** Every shortcut, for the sheet "?" opens. */
export function shortcuts(mac = isMac()): [string, [string, string][]][] {
  const m = mac ? "⌘" : "Ctrl+";
  return [
    [
      "Anywhere",
      [
        [`${m}F`, "Search"],
        [`${m}[`, "Back"],
        [`${m}]`, "Forward"],
        [`${m}1`, "Home"],
        [`${m}2`, "Movies"],
        [`${m}3`, "TV"],
        [`${m}4`, "Music"],
        [`${m}5`, "YouTube"],
        [`${m}6`, "Stations"],
        ["?", "Keyboard shortcuts"],
      ],
    ],
    [
      "Player",
      [
        ["Space", "Play or pause"],
        ["← →", "Back or ahead 10 seconds"],
        ["↑ ↓", "Volume"],
        ["F", "Full screen"],
        ["M", "Mute"],
        ["C", "Next subtitles"],
        ["Esc", "Close the player"],
      ],
    ],
  ];
}

export function helpSheet(mac = isMac()): HTMLElement {
  const scrim = h("div.scrim.keys");
  const close = () => scrim.remove();
  const done = h("button.btn.s", { type: "button", onclick: close }, "Done");
  scrim.append(
    h(
      "div.dialog.keysheet",
      { role: "dialog", "aria-modal": "true", "aria-labelledby": "kt" },
      h("h2", { id: "kt" }, "Keyboard shortcuts"),
      ...shortcuts(mac).map(([group, rows]) =>
        h(
          "section",
          h("h3", group),
          h("dl", ...rows.flatMap(([k, does]) => [h("dt", h("kbd", k)), h("dd", does)])),
        ),
      ),
      done,
    ),
  );
  scrim.addEventListener("click", (e) => e.target === scrim && close());
  scrim.addEventListener("keydown", (e) => e.key === "Escape" && (e.stopPropagation(), close()));
  queueMicrotask(() => done.focus());
  return scrim;
}
