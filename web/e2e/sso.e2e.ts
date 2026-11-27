import { expect, test } from "@playwright/test";
import { createHash, generateKeyPairSync, randomBytes, sign } from "node:crypto";
import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { api, email, seed } from "./support";

const b64 = (b: Buffer | string) => Buffer.from(b).toString("base64url");

/**
 * An OpenID provider that signs in the seeded adult at once: discovery, JWKS,
 * and a token endpoint that checks the PKCE verifier and redirect URI.
 */
function provider() {
  const { publicKey, privateKey } = generateKeyPairSync("rsa", { modulusLength: 2048 });
  const grants = new Map<string, { challenge: string; redirect: string; nonce: string }>();
  let issuer = "";
  const server: Server = createServer((req, res) => {
    const url = new URL(req.url!, issuer);
    const json = (status: number, body: unknown) =>
      res.writeHead(status, { "content-type": "application/json" }).end(JSON.stringify(body));
    if (url.pathname === "/.well-known/openid-configuration")
      return json(200, {
        issuer,
        authorization_endpoint: `${issuer}/authorize`,
        token_endpoint: `${issuer}/token`,
        jwks_uri: `${issuer}/jwks`,
        id_token_signing_alg_values_supported: ["RS256"],
      });
    if (url.pathname === "/jwks")
      return json(200, {
        keys: [{ ...publicKey.export({ format: "jwk" }), kid: "k1", alg: "RS256" }],
      });
    if (url.pathname === "/authorize") {
      const q = url.searchParams;
      const code = b64(randomBytes(16));
      grants.set(code, {
        challenge: q.get("code_challenge")!,
        redirect: q.get("redirect_uri")!,
        nonce: q.get("nonce")!,
      });
      const to = new URL(q.get("redirect_uri")!);
      to.search = new URLSearchParams({ code, state: q.get("state")! }).toString();
      return res.writeHead(302, { location: to.toString() }).end();
    }
    let body = "";
    req.on("data", (c) => (body += c));
    req.on("end", () => {
      const f = new URLSearchParams(body);
      const g = grants.get(f.get("code")!);
      grants.delete(f.get("code")!);
      const verifier = b64(
        createHash("sha256")
          .update(f.get("code_verifier") ?? "")
          .digest(),
      );
      if (!g || verifier !== g.challenge || f.get("redirect_uri") !== g.redirect)
        return json(400, { error: "invalid_grant" });
      const now = Math.floor(Date.now() / 1000);
      const head = b64(JSON.stringify({ alg: "RS256", kid: "k1", typ: "JWT" }));
      const claims = b64(
        JSON.stringify({
          iss: issuer,
          aud: "rabbit-ears",
          sub: "e2e-ada",
          email,
          email_verified: true,
          nonce: g.nonce,
          iat: now,
          exp: now + 300,
        }),
      );
      const sig = b64(sign("RSA-SHA256", Buffer.from(`${head}.${claims}`), privateKey));
      json(200, {
        access_token: "at",
        token_type: "Bearer",
        expires_in: 300,
        id_token: `${head}.${claims}.${sig}`,
      });
    });
  });
  return new Promise<{ issuer: string; close: () => void }>((done) =>
    server.listen(0, "127.0.0.1", () => {
      issuer = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
      done({ issuer, close: () => server.close() });
    }),
  );
}

test("an adult signs in with a single sign-on provider", async ({ page, request }) => {
  const headers = await seed(request);
  const idp = await provider();
  const created = await request.post(`${api}/admin/sso-providers`, {
    headers,
    data: { name: "Keyhole", issuer: idp.issuer, clientId: "rabbit-ears", linksByEmail: true },
  });
  expect(created.ok()).toBeTruthy();
  const { id } = await created.json();
  try {
    await page.goto("/");
    await page.getByRole("link", { name: "Sign in with Keyhole" }).click();
    await expect(page.getByRole("heading", { name: "Who's watching?" })).toBeVisible();
    expect(new URL(page.url()).hash).toBe("#/who");
  } finally {
    await request.delete(`${api}/admin/sso-providers/${id}`, { headers });
    idp.close();
  }
});
