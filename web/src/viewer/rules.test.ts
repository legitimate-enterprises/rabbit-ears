// The rule editor on its own: the fields each kind offers, the operators and values each
// field takes, client checks, the body it saves and where a 400 lands.
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../admin/api";
import type { Schemas } from "../api/client";
import { createSmart, rulesDialog, type Draft, type RulesKind } from "./rules";

type Rules = Schemas["SmartRules"];
const text = (els: Iterable<Element>) => [...els].map((e) => e.textContent);
const dlg = () => document.querySelector<HTMLElement>("[role=dialog]")!;
const rows = () => [...dlg().querySelectorAll<HTMLElement>("[role=group][aria-label^=Condition]")];
const sel = (r: HTMLElement, label: string) =>
  r.querySelector<HTMLSelectElement>(`select[aria-label=${label}]`)!;
const value = (r: HTMLElement) => r.querySelector<HTMLInputElement>("input[aria-label=Value]");
const button = (r: ParentNode, label: string) =>
  [...r.querySelectorAll<HTMLElement>("button")].find(
    (b) => b.textContent === label || b.getAttribute("aria-label") === label,
  )!;
const pick = (s: HTMLSelectElement, v: string) => {
  s.value = v;
  s.dispatchEvent(new Event("change"));
};
const type = (i: HTMLInputElement, v: string) => {
  i.value = v;
  i.dispatchEvent(new Event("input"));
};
const submit = () => dlg().querySelector("form")!.requestSubmit();

const open = (kind: RulesKind, extra: Partial<Parameters<typeof rulesDialog>[0]> = {}) => {
  const save = vi.fn<(d: Draft) => Promise<void>>(async () => {});
  rulesDialog({
    heading: "New smart playlist",
    kind,
    name: "",
    kindChoice: "choose",
    save,
    ...extra,
  });
  return save;
};

beforeEach(() => document.body.replaceChildren());

describe("fields", () => {
  it("offers each kind its own fields, and a collection none of the owner's", () => {
    const labels = (kind: RulesKind) => {
      document.body.replaceChildren();
      open(kind, kind === "collection" ? { kindChoice: "none" } : {});
      return text(sel(rows()[0], "Field").options);
    };
    expect(labels("movies")).toEqual([
      "Title",
      "Year",
      "Runtime",
      "Content rating",
      "Genre",
      "Cast",
      "Director",
      "HDR",
      "4K",
      "Watched",
      "Plays",
      "Favorite",
    ]);
    expect(labels("tv")).toEqual([
      "Title",
      "Show",
      "Season",
      "Episode",
      "Year",
      "Runtime",
      "Content rating",
      "Genre",
      "Cast",
      "HDR",
      "4K",
      "Watched",
      "Plays",
      "Favorite",
    ]);
    expect(labels("music")).toEqual([
      "Title",
      "Artist",
      "Album",
      "Year",
      "Genre",
      "Duration",
      "Explicit",
      "Lossless",
      "Plays",
      "Favorite",
    ]);
    expect(labels("collection")).toEqual([
      "Title",
      "Year",
      "Runtime",
      "Content rating",
      "Genre",
      "Cast",
      "Director",
      "HDR",
      "4K",
    ]);
    expect(dlg().querySelector("select[aria-label=Kind]")).toBeNull();
  });

  it("gives text, number and yes/no fields their operators and values", () => {
    open("movies");
    const [r] = rows();
    expect(text(sel(r, "Operator").options)).toEqual([
      "is",
      "is not",
      "contains",
      "does not contain",
      "starts with",
      "ends with",
    ]);
    expect(value(r)!.type).toBe("text");
    pick(sel(r, "Field"), "runtime");
    const [n] = rows();
    expect(text(sel(n, "Operator").options)).toEqual([
      "=",
      "≠",
      "less than",
      "at most",
      "more than",
      "at least",
    ]);
    expect(value(n)!.inputMode).toBe("numeric");
    expect(n.querySelector(".unit")!.textContent).toBe("min");
    pick(sel(n, "Field"), "hdr");
    const [b] = rows();
    expect(text(sel(b, "Operator").options)).toEqual(["Yes", "No"]);
    expect(value(b)).toBeNull();
    pick(sel(b, "Field"), "contentRating");
    const list = dlg().querySelector<HTMLDataListElement>(
      `#${value(rows()[0])!.getAttribute("list")}`,
    )!;
    expect(text(list.options)).toContain("PG-13");
  });

  it("resets the operator on a field change and keeps only a value of the same type", () => {
    open("movies");
    pick(sel(rows()[0], "Operator"), "endsWith");
    type(value(rows()[0])!, "Dune");
    pick(sel(rows()[0], "Field"), "genre");
    expect(sel(rows()[0], "Operator").value).toBe("is");
    expect(value(rows()[0])!.value).toBe("Dune");
    expect(document.activeElement).toBe(sel(rows()[0], "Field"));
    pick(sel(rows()[0], "Field"), "year");
    expect(sel(rows()[0], "Operator").value).toBe("is");
    expect(value(rows()[0])!.value).toBe("");
  });

  it("notes the owner's fields only while one is used", () => {
    open("movies");
    const note = "Watched, plays and favorites are yours, the owner's.";
    expect(dlg().textContent).not.toContain(note);
    pick(sel(rows()[0], "Field"), "favorite");
    expect(dlg().textContent).toContain(note);
  });
});

describe("groups", () => {
  it("reads as conditions with one group, and as groups once there are two", () => {
    open("movies");
    const line = () => dlg().querySelector(".matchline")!.textContent;
    expect(line()).toBe("Match allany of these conditions");
    button(dlg(), "+ Group").click();
    expect(line()).toBe("Match allany of these groups");
    expect(text(dlg().querySelectorAll(".rulecard > .cardline"))).toEqual([
      "allany of:",
      "allany of:",
    ]);
    // Its only condition's − takes the second card with it.
    button(dlg().querySelectorAll(".rulecard")[1] as HTMLElement, "Remove condition").click();
    expect(dlg().querySelectorAll(".rulecard")).toHaveLength(1);
    expect(line()).toBe("Match allany of these conditions");
    expect(button(dlg(), "Remove condition").hidden).toBe(true);
  });
});

describe("saving", () => {
  it("refuses a blank name, a blank text and a number that is not whole", async () => {
    const save = open("movies");
    submit();
    expect(dlg().querySelector(".field .err, .err")).toBeTruthy();
    expect(text(dlg().querySelectorAll(".err:not([hidden])"))).toEqual([
      "Enter a name.",
      "Enter a value.",
    ]);
    pick(sel(rows()[0], "Field"), "year");
    type(value(rows()[0])!, "19.5");
    submit();
    expect(text(rows()[0].parentElement!.querySelectorAll(".err:not([hidden])"))).toContain(
      "Enter a whole number.",
    );
    expect(save).not.toHaveBeenCalled();
  });

  it("saves the name, kind and rules as the API takes them", async () => {
    const save = open("movies");
    type(dlg().querySelector<HTMLInputElement>("input[name=name]")!, " Late night ");
    pick(dlg().querySelector<HTMLSelectElement>("select[aria-label=Kind]")!, "tv");
    pick(sel(rows()[0], "Field"), "show");
    pick(sel(rows()[0], "Operator"), "startsWith");
    type(value(rows()[0])!, "  Star ");
    button(dlg(), "+ Condition").click();
    pick(sel(rows()[1], "Field"), "season");
    pick(sel(rows()[1], "Operator"), "ge");
    type(value(rows()[1])!, "2");
    pick(dlg().querySelector<HTMLSelectElement>(".matchline select")!, "any");
    button(dlg(), "+ Group").click();
    pick(sel(rows()[2], "Field"), "watched");
    pick(sel(rows()[2], "Operator"), "isFalse");
    pick(dlg().querySelector<HTMLSelectElement>(".matchline select")!, "all");
    submit();
    await vi.waitFor(() => expect(save).toHaveBeenCalled());
    expect(save.mock.calls[0][0]).toEqual({
      name: "Late night",
      kind: "tv",
      rules: {
        match: "all",
        groups: [
          {
            match: "any",
            conditions: [
              { field: "show", operator: "startsWith", text: "Star" },
              { field: "season", operator: "ge", number: 2 },
            ],
          },
          { match: "all", conditions: [{ field: "watched", operator: "isFalse" }] },
        ],
      } satisfies Rules,
    });
    await vi.waitFor(() => expect(document.querySelector("[role=dialog]")).toBeNull());
  });

  it("opens on saved rules, its kind locked, and drops fields the kind lacks on a kind change", () => {
    const rules: Rules = {
      match: "any",
      groups: [
        { match: "all", conditions: [{ field: "director", operator: "is", text: "Lynch" }] },
        { match: "any", conditions: [{ field: "uhd", operator: "isTrue" }] },
      ],
    };
    open("movies", { rules, kindChoice: "locked", name: "Weird" });
    expect(dlg().querySelector<HTMLSelectElement>("select[aria-label=Kind]")!.disabled).toBe(true);
    expect(dlg().querySelector<HTMLSelectElement>(".matchline select")!.value).toBe("any");
    expect(sel(rows()[0], "Field").value).toBe("director");
    expect(value(rows()[0])!.value).toBe("Lynch");
    expect(sel(rows()[1], "Operator").value).toBe("isTrue");

    document.body.replaceChildren();
    open("movies", { rules });
    pick(dlg().querySelector<HTMLSelectElement>("select[aria-label=Kind]")!, "music");
    expect(sel(rows()[0], "Field").value).toBe("title");
    expect(value(rows()[0])!.value).toBe("");
    expect(sel(rows()[1], "Field").value).toBe("title");
  });

  it("puts a 400's field errors under their rows and stays open", async () => {
    const save = open("movies", {
      name: "Picks",
      save: vi.fn(async () => {
        throw new ApiError("bad", 400, [
          { field: "groups.1.conditions.0.text", message: "too long" },
          { field: "name", message: "taken" },
        ]);
      }),
    });
    void save;
    type(value(rows()[0])!, "a");
    button(dlg(), "+ Group").click();
    type(value(rows()[1])!, "b");
    submit();
    await vi.waitFor(() =>
      expect(rows()[1].parentElement!.querySelector(".err:not([hidden])")?.textContent).toBe(
        "too long",
      ),
    );
    expect(rows()[0].parentElement!.querySelector(".err:not([hidden])")).toBeNull();
    expect(dlg().querySelector(".end")!.previousElementSibling!.textContent).toBe("name: taken");
  });
});

describe("createSmart", () => {
  it("deletes the list it made when the rules are refused, and says why", async () => {
    const remove = vi.fn(async () => {});
    const refused = new ApiError("bad", 400, [
      { field: "groups.0.conditions.0.text", message: "x" },
    ]);
    await expect(
      createSmart(
        async () => ({ id: "n" }),
        async () => {
          throw refused;
        },
        remove,
      ),
    ).rejects.toBe(refused);
    expect(remove).toHaveBeenCalledWith("n");
    const made = await createSmart(
      async () => ({ id: "m" }),
      async () => {},
      remove,
    );
    expect(made).toEqual({ id: "m" });
    expect(remove).toHaveBeenCalledTimes(1);
  });
});
