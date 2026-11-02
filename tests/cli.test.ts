import { afterEach, expect, test } from "bun:test";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { run } from "../src/run.js";

const servers: ReturnType<typeof Bun.serve>[] = [];
const temporaryDirectories: string[] = [];

afterEach(() => {
  for (const server of servers.splice(0)) server.stop(true);
  for (const directory of temporaryDirectories.splice(0)) rmSync(directory, { recursive: true, force: true });
});

function api(handler: (request: Request) => Promise<Response> | Response) {
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, fetch: handler });
  servers.push(server);
  return `http://127.0.0.1:${server.port}/v1`;
}

async function invoke(args: string[], baseURL: string, key = "test-key") {
  const output: (string | Uint8Array)[] = [];
  const errors: string[] = [];
  const status = await run(args, {
    env: { STRING_API_KEY: key, STRING_API_BASE_URL: baseURL },
    request: (url, init) => fetch(url, init),
    stdout: (data) => output.push(data),
    stderr: (data) => errors.push(data)
  });
  return { status, output: output.map(String).join(""), errors: errors.join("") };
}

test("fetch sends authenticated JSON and preserves advanced request fields", async () => {
  let received: { path: string; authorization: string | null; body: unknown } | undefined;
  const baseURL = api(async (request) => {
    received = {
      path: new URL(request.url).pathname,
      authorization: request.headers.get("authorization"),
      body: await request.json()
    };
    return Response.json({ statusCode: 200, data: { title: "Example" } });
  });
  const directory = mkdtempSync(join(tmpdir(), "string-cli-test-"));
  temporaryDirectories.push(directory);
  const input = join(directory, "options.json");
  writeFileSync(input, JSON.stringify({ mainContentOnly: true }));

  const result = await invoke(["fetch", "https://example.com", "--input", input], baseURL);

  expect(result.status).toBe(0);
  expect(JSON.parse(result.output)).toEqual({ statusCode: 200, data: { title: "Example" } });
  expect(received).toEqual({
    path: "/v1/fetch",
    authorization: "Bearer test-key",
    body: { mainContentOnly: true, url: "https://example.com/", method: "GET", format: "json" }
  });
});

test("search carries targeting and result count to the search API", async () => {
  let received: unknown;
  const baseURL = api(async (request) => {
    expect(new URL(request.url).pathname).toBe("/v1/search");
    received = await request.json();
    return Response.json({ results: [], zeroResults: true });
  });

  const result = await invoke(["search", "coffee near me", "--engine", "google", "--location", "Austin", "--count", "20"], baseURL);

  expect(result.status).toBe(0);
  expect(received).toEqual({ query: "coffee near me", engine: "google", location: "Austin", searchCount: 20 });
  expect(JSON.parse(result.output)).toEqual({ results: [], zeroResults: true });
});

test("sitemap submission only quotes; approval is a separate command", async () => {
  const calls: string[] = [];
  const baseURL = api((request) => {
    calls.push(`${request.method} ${new URL(request.url).pathname}`);
    return Response.json({ jobId: "job-1", status: calls.length === 1 ? "awaiting_approval" : "running" });
  });

  const quote = await invoke(["sitemap", "submit", "https://example.com"], baseURL);
  expect(quote.status).toBe(0);
  expect(JSON.parse(quote.output).status).toBe("awaiting_approval");
  expect(calls).toEqual(["POST /v1/sitemap"]);

  const approval = await invoke(["sitemap", "approve", "job-1"], baseURL);
  expect(approval.status).toBe(0);
  expect(calls).toEqual(["POST /v1/sitemap", "POST /v1/sitemap/job-1/approve"]);
});

test("request sends the destination method and body through fetch", async () => {
  let received: unknown;
  const baseURL = api(async (request) => {
    expect(new URL(request.url).pathname).toBe("/v1/fetch");
    received = await request.json();
    return Response.json({ statusCode: 201, data: { saved: true } });
  });
  const directory = mkdtempSync(join(tmpdir(), "string-cli-test-"));
  temporaryDirectories.push(directory);
  const input = join(directory, "options.json");
  writeFileSync(input, JSON.stringify({ body: { name: "example" }, headers: { "Content-Type": "application/json" } }));

  const result = await invoke(["request", "POST", "https://example.com/api", "--input", input], baseURL);

  expect(result.status).toBe(0);
  expect(received).toEqual({
    url: "https://example.com/api",
    method: "POST",
    format: "json",
    body: { name: "example" },
    headers: { "Content-Type": "application/json" }
  });
  expect(JSON.parse(result.output)).toEqual({ statusCode: 201, data: { saved: true } });
});

test("sitemap results sends pagination as query parameters", async () => {
  const baseURL = api((request) => {
    expect(new URL(request.url).pathname).toBe("/v1/sitemap/job-1/urls");
    expect(new URL(request.url).searchParams.get("limit")).toBe("100");
    expect(new URL(request.url).searchParams.get("offset")).toBe("5");
    return Response.json({ jobId: "job-1", total: 0, urls: [] });
  });

  const result = await invoke(["sitemap", "results", "job-1", "--limit", "100", "--offset", "5"], baseURL);

  expect(result.status).toBe(0);
  expect(JSON.parse(result.output).urls).toEqual([]);
});

test("fetch --format overrides a raw input format for output decoding too", async () => {
  let received: { format?: string } | undefined;
  const baseURL = api(async (request) => {
    received = await request.json();
    return new Response('{"ok":true}');
  });
  const directory = mkdtempSync(join(tmpdir(), "string-cli-test-"));
  temporaryDirectories.push(directory);
  const input = join(directory, "options.json");
  writeFileSync(input, JSON.stringify({ format: "raw" }));

  const result = await invoke(["fetch", "https://example.com", "--input", input, "--format", "json"], baseURL);

  expect(result.status).toBe(0);
  expect(received?.format).toBe("json");
  expect(result.output).toBe('{"ok":true}\n');
});

test("API base URLs with empty query or fragment delimiters are rejected", async () => {
  const baseURL = api(() => Response.json({}));
  for (const suffix of ["?", "#"]) {
    const result = await invoke(["fetch", "https://example.com"], `${baseURL}${suffix}`);
    expect(result.status).toBe(1);
    expect(result.errors).toContain("STRING_API_BASE_URL must be an HTTP(S) URL");
  }
});

test("missing key and API errors fail without printing the key", async () => {
  const baseURL = api(() => new Response('{"error":"bad key"}', { status: 401 }));
  const missing = await invoke(["fetch", "https://example.com"], baseURL, "");
  expect(missing.status).toBe(1);
  expect(missing.errors).toContain("Set STRING_API_KEY");

  const rejected = await invoke(["fetch", "https://example.com"], baseURL, "private-key");
  expect(rejected.status).toBe(1);
  expect(rejected.errors).toContain("HTTP 401");
  expect(rejected.errors).not.toContain("private-key");

  const malformed = await invoke(["fetch", "https://example.com"], baseURL, "private\nkey");
  expect(malformed.status).toBe(1);
  expect(malformed.errors).toContain("STRING_API_KEY contains characters");
  expect(malformed.errors).not.toContain("private");
});
