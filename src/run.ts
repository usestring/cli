import { readFileSync } from "node:fs";
import { parseArgs, type ParseArgsOptionsConfig } from "node:util";

type Runtime = {
  env: Record<string, string | undefined>;
  request: (url: string, init: RequestInit) => Promise<Response>;
  stdout: (data: string | Uint8Array) => void;
  stderr: (data: string) => void;
};

type Body = Record<string, unknown>;

const HELP = `String Web Access CLI

Usage:
  string fetch URL [--format json|markdown|raw] [--input FILE]
  string request METHOD URL [--body TEXT|--body-file FILE] [--input FILE]
  string search QUERY [--engine NAME] [--country CODE] [--count N] [--input FILE]
  string sitemap submit URL [--input FILE]
  string sitemap approve JOB_ID
  string sitemap status JOB_ID
  string sitemap results JOB_ID [--limit N] [--offset N]
  string sitemap list [--limit N] [--offset N]
  string sitemap cancel JOB_ID

Set STRING_API_KEY to an API key from https://portal.usestring.ai/settings.
Set STRING_API_BASE_URL only when targeting a different API endpoint.
--input reads additional request fields from a JSON object in FILE, or from stdin with -.
Responses are written to stdout as JSON by default. Fetch and request also support
markdown or raw output with --format. Errors go to stderr and return a nonzero status.
`;

const commonOptions = {
  help: { type: "boolean" },
  input: { type: "string" }
} as const satisfies ParseArgsOptionsConfig;

function argumentsFor<T extends ParseArgsOptionsConfig>(args: string[], options: T) {
  return parseArgs({ args, options, allowPositionals: true, strict: true });
}

function expectPositionals(positionals: string[], count: number, usage: string): void {
  if (positionals.length !== count || positionals.some((value) => value.length === 0)) {
    throw new Error(`Usage: ${usage}`);
  }
}

function jsonObject(path: string | undefined): Body {
  if (!path) return {};
  const source = path === "-" ? readFileSync(0, "utf8") : readFileSync(path, "utf8");
  const value: unknown = JSON.parse(source);
  if (!isBody(value)) throw new Error("--input must contain a JSON object");
  return value;
}

function isBody(value: unknown): value is Body {
  return value !== null && !Array.isArray(value) && typeof value === "object";
}

function positiveInteger(value: string | undefined, name: string): number | undefined {
  if (value === undefined) return undefined;
  const parsed = Number(value);
  if (!Number.isSafeInteger(parsed) || parsed < 1) throw new Error(`${name} must be a positive integer`);
  return parsed;
}

function nonnegativeInteger(value: string | undefined, name: string): number | undefined {
  if (value === undefined) return undefined;
  const parsed = Number(value);
  if (!Number.isSafeInteger(parsed) || parsed < 0) throw new Error(`${name} must be a nonnegative integer`);
  return parsed;
}

function fetchFormat(value: string | undefined): "json" | "markdown" | "raw" {
  if (value === undefined || value === "json") return "json";
  if (value === "markdown" || value === "raw") return value;
  throw new Error("--format must be json, markdown, or raw");
}

function targetURL(value: string): string {
  const url = new URL(value);
  if (url.protocol !== "https:" && url.protocol !== "http:") {
    throw new Error("URL must use http or https");
  }
  return url.toString();
}

function apiBaseURL(env: Runtime["env"]): string {
  const url = new URL(env.STRING_API_BASE_URL ?? "https://request.usestring.ai/v1");
  if (!["http:", "https:"].includes(url.protocol) || /[?#]/.test(url.href) || url.username || url.password) {
    throw new Error("STRING_API_BASE_URL must be an HTTP(S) URL without credentials, query, or fragment");
  }
  return url.toString().replace(/\/$/, "");
}

function fetchCommand(args: string[]) {
  const { values, positionals } = argumentsFor(args, {
    ...commonOptions,
    format: { type: "string" }
  });
  if (values.help) return null;
  expectPositionals(positionals, 1, "string fetch URL [--format json|markdown|raw] [--input FILE]");
  const body = jsonObject(values.input);
  if (body.method && String(body.method).toUpperCase() !== "GET") {
    throw new Error("Use string request for POST, PUT, or PATCH");
  }
  if (body.body !== undefined) throw new Error("Fetch cannot send a body; use string request");
  const format = fetchFormat(values.format ?? String(body.format ?? "json"));
  return {
    method: "POST",
    path: "/fetch",
    body: { ...body, url: targetURL(positionals[0]), method: "GET", format },
    binary: format === "raw"
  };
}

function requestCommand(args: string[]) {
  const { values, positionals } = argumentsFor(args, {
    ...commonOptions,
    format: { type: "string" },
    body: { type: "string" },
    "body-file": { type: "string" }
  });
  if (values.help) return null;
  expectPositionals(positionals, 2, "string request METHOD URL [--body TEXT|--body-file FILE] [--input FILE]");
  const method = positionals[0].toUpperCase();
  if (!["POST", "PUT", "PATCH"].includes(method)) throw new Error("Request method must be POST, PUT, or PATCH");
  if (values.body !== undefined && values["body-file"] !== undefined) {
    throw new Error("Use only one of --body and --body-file");
  }
  const body = jsonObject(values.input);
  const requestBody = values["body-file"] === undefined ? values.body : readFileSync(values["body-file"], "utf8");
  const format = fetchFormat(values.format ?? String(body.format ?? "json"));
  return {
    method: "POST",
    path: "/fetch",
    body: {
      ...body,
      url: targetURL(positionals[1]),
      method,
      format,
      ...(requestBody === undefined ? {} : { body: requestBody })
    },
    binary: format === "raw"
  };
}

function searchCommand(args: string[]) {
  const { values, positionals } = argumentsFor(args, {
    ...commonOptions,
    engine: { type: "string" },
    country: { type: "string" },
    language: { type: "string" },
    count: { type: "string" },
    location: { type: "string" }
  });
  if (values.help) return null;
  expectPositionals(positionals, 1, "string search QUERY [--engine NAME] [--country CODE] [--count N] [--input FILE]");
  return {
    method: "POST",
    path: "/search",
    body: {
      ...jsonObject(values.input),
      query: positionals[0],
      ...(values.engine === undefined ? {} : { engine: values.engine }),
      ...(values.country === undefined ? {} : { country: values.country }),
      ...(values.language === undefined ? {} : { language: values.language }),
      ...(values.location === undefined ? {} : { location: values.location }),
      ...(values.count === undefined ? {} : { searchCount: positiveInteger(values.count, "--count") })
    },
    binary: false
  };
}

function sitemapCommand(args: string[]) {
  const [action, ...rest] = args;
  if (!action || action === "--help") return null;
  const { values, positionals } = argumentsFor(rest, {
    ...commonOptions,
    limit: { type: "string" },
    offset: { type: "string" }
  });
  if (values.help) return null;
  const root = "/sitemap";
  if (action === "submit") {
    expectPositionals(positionals, 1, "string sitemap submit URL [--input FILE]");
    if (values.limit !== undefined || values.offset !== undefined) throw new Error("--limit and --offset apply to list or results");
    return { method: "POST", path: root, body: { ...jsonObject(values.input), url: targetURL(positionals[0]) }, binary: false };
  }
  if (values.input !== undefined) throw new Error("--input applies only to sitemap submit");
  const isList = action === "list";
  expectPositionals(positionals, isList ? 0 : 1, `string sitemap ${action}${isList ? "" : " JOB_ID"}`);
  const id = isList ? "" : `/${encodeURIComponent(positionals[0])}`;
  if (action === "approve" || action === "status" || action === "cancel") {
    if (values.limit !== undefined || values.offset !== undefined) throw new Error("--limit and --offset apply to list or results");
    return {
      method: action === "status" ? "GET" : action === "cancel" ? "DELETE" : "POST",
      path: `${root}${id}${action === "approve" ? "/approve" : ""}`,
      binary: false
    };
  }
  if (action === "results" || isList) {
    const query = new URLSearchParams();
    const limit = positiveInteger(values.limit, "--limit");
    const offset = nonnegativeInteger(values.offset, "--offset");
    if (limit !== undefined) query.set("limit", String(limit));
    if (offset !== undefined) query.set("offset", String(offset));
    const suffix = query.size ? `?${query}` : "";
    return { method: "GET", path: `${root}${id}${action === "results" ? "/urls" : ""}${suffix}`, binary: false };
  }
  throw new Error(`Unknown sitemap command: ${action}`);
}

function operationFor(command: string, args: string[]) {
  switch (command) {
    case "fetch":
      return fetchCommand(args);
    case "request":
      return requestCommand(args);
    case "search":
      return searchCommand(args);
    case "sitemap":
      return sitemapCommand(args);
    default:
      throw new Error(`Unknown command: ${command}`);
  }
}

export async function run(args: string[], runtime: Runtime): Promise<number> {
  try {
    const [command, ...rest] = args;
    if (!command || command === "--help" || command === "-h" || command === "help") {
      runtime.stdout(HELP);
      return 0;
    }
    const operation = operationFor(command, rest);
    if (!operation) {
      runtime.stdout(HELP);
      return 0;
    }
    const key = runtime.env.STRING_API_KEY;
    if (!key) throw new Error("Set STRING_API_KEY to a String Web Access API key");
    if (!/^[\x21-\x7e]+$/.test(key)) throw new Error("STRING_API_KEY contains characters that are not valid in an API key");
    const response = await runtime.request(`${apiBaseURL(runtime.env)}${operation.path}`, {
      method: operation.method,
      headers: {
        Authorization: `Bearer ${key}`,
        ...(operation.body === undefined ? {} : { "Content-Type": "application/json" })
      },
      ...(operation.body === undefined ? {} : { body: JSON.stringify(operation.body) })
    });
    if (!response.ok) {
      const detail = (await response.text()).slice(0, 2000);
      runtime.stderr(`HTTP ${response.status}${detail ? `: ${detail}` : ""}\n`);
      return 1;
    }
    const data = new Uint8Array(await response.arrayBuffer());
    if (operation.binary) {
      runtime.stdout(data);
    } else {
      const text = new TextDecoder().decode(data);
      runtime.stdout(text.endsWith("\n") ? text : `${text}\n`);
    }
    return 0;
  } catch (error) {
    runtime.stderr(`${error instanceof Error ? error.message : String(error)}\n`);
    return 1;
  }
}
