// Builds the npm packages from a goreleaser dist directory: one package per
// platform holding its binary, and @usestring/cli, which depends on all of
// them as optionalDependencies so npm installs only the one that fits.
//
// Usage: node npm/build.mjs DIST_DIR OUT_DIR
import { copyFileSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { basename, dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const [dist, out] = process.argv.slice(2);
if (!dist || !out) {
  console.error("Usage: node npm/build.mjs DIST_DIR OUT_DIR");
  process.exit(2);
}
const root = dirname(dirname(fileURLToPath(import.meta.url)));
const { version } = JSON.parse(readFileSync(join(dist, "metadata.json"), "utf8"));
const platforms = { darwin: "darwin", linux: "linux", windows: "win32" };
const cpus = { amd64: "x64", arm64: "arm64" };
const common = {
  version,
  license: "MIT",
  homepage: "https://usestring.ai",
  repository: { type: "git", url: "git+https://github.com/usestring/cli.git" },
};

function writePackage(dir, manifest) {
  writeFileSync(join(dir, "package.json"), `${JSON.stringify(manifest, null, 2)}\n`);
}

const optionalDependencies = {};
for (const artifact of JSON.parse(readFileSync(join(dist, "artifacts.json"), "utf8"))) {
  if (artifact.type !== "Binary") continue;
  const os = platforms[artifact.goos];
  const cpu = cpus[artifact.goarch];
  const name = `@usestring/cli-${os}-${cpu}`;
  const dir = join(out, `cli-${os}-${cpu}`);
  mkdirSync(join(dir, "bin"), { recursive: true });
  copyFileSync(join(root, artifact.path), join(dir, "bin", basename(artifact.path)));
  writePackage(dir, {
    name,
    ...common,
    description: `The ${os}-${cpu} binary for @usestring/cli`,
    os: [os],
    cpu: [cpu],
    files: ["bin"],
    preferUnplugged: true,
  });
  optionalDependencies[name] = version;
}

const main = join(out, "cli");
mkdirSync(join(main, "bin"), { recursive: true });
copyFileSync(join(root, "npm", "bin", "string.js"), join(main, "bin", "string.js"));
copyFileSync(join(root, "README.md"), join(main, "README.md"));
writePackage(main, {
  name: "@usestring/cli",
  ...common,
  description: "Command-line access to String Web Access",
  keywords: ["web-access", "web-scraping", "search", "cli", "agent-skills"],
  bin: { string: "bin/string.js" },
  files: ["bin", "README.md"],
  optionalDependencies,
  engines: { node: ">=18" },
});
console.log(`Built @usestring/cli ${version} with ${Object.keys(optionalDependencies).length} platform packages in ${out}`);
