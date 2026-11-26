import { existsSync, readFileSync } from "node:fs";
import { resolve } from "node:path";
import { expect, test } from "vitest";

const web = resolve(import.meta.dirname, "..");

// A path with no file behind it answers with the page, so a page naming an
// icon that is not shipped, or a tool asking for /favicon.ico, would get HTML.
test.each(["index.html", "admin/index.html"])("%s names icons the site ships", (page) => {
  const html = new DOMParser().parseFromString(
    readFileSync(resolve(web, page), "utf8"),
    "text/html",
  );
  const hrefs = [...html.querySelectorAll("link[rel~=icon], link[rel=apple-touch-icon]")].map(
    (link) => link.getAttribute("href") ?? "",
  );
  expect(hrefs).toEqual(["/favicon.ico", "/icon.png", "/icon.png"]);
  for (const href of hrefs) expect(existsSync(resolve(web, "public", href.slice(1)))).toBe(true);
});
