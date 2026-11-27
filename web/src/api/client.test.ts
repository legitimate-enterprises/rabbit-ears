import { describe, expect, it } from "vitest";
import { createApi, type Schemas } from "./client";
import { uid } from "./uid";

function recorder(status: number, body: unknown) {
  const seen: Request[] = [];
  const fetch = async (req: Request) => {
    seen.push(req);
    return new Response(JSON.stringify(body), {
      status,
      headers: { "Content-Type": "application/json" },
    });
  };
  return { seen, fetch };
}

describe("generated API client", () => {
  it("prefixes /api/v2 and sends the device token", async () => {
    const page: Schemas["LibraryPage"] = { items: [] };
    const { seen, fetch } = recorder(200, page);
    const api = createApi({ token: "tok", baseUrl: "http://server/api/v2", fetch });
    const { data } = await api.GET("/libraries", { params: { query: { limit: 10 } } });
    expect(data?.items).toEqual([]);
    expect(seen[0].url).toBe("http://server/api/v2/libraries?limit=10");
    expect(seen[0].headers.get("Authorization")).toBe("Bearer tok");
  });

  it("surfaces the shared error shape", async () => {
    const err: Schemas["Error"] = { code: "forbidden", message: "not allowed" };
    const { fetch } = recorder(403, err);
    const api = createApi({ baseUrl: "http://server/api/v2", fetch });
    const { error, response } = await api.DELETE("/libraries/{libraryId}", {
      params: { path: { libraryId: uid(1) } },
    });
    expect(response.status).toBe(403);
    expect(error?.code).toBe("forbidden");
  });

  it("sends one capability group alone mid-playback", async () => {
    const { seen, fetch } = recorder(200, {});
    const api = createApi({ token: "t", baseUrl: "http://server/api/v2", fetch });
    const display: Schemas["DisplayCapabilities"] = {
      width: 1920,
      height: 1080,
      refreshRate: 60,
      hdrFormats: [],
    };
    await api.PUT("/playback/sessions/{sessionId}/display", {
      params: { path: { sessionId: uid(7) } },
      body: display,
    });
    expect(seen[0].method).toBe("PUT");
    expect(seen[0].url).toBe(`http://server/api/v2/playback/sessions/${uid(7)}/display`);
    expect(await seen[0].json()).toEqual(display);
  });
});
