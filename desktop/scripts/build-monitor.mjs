// Builds the monitor binary the app bundles (desktop/bin/monitor), from this
// same checkout, so the app always ships the protocol version it was built
// against. --universal builds darwin arm64 + amd64 and merges them with
// lipo, for a universal .app.
import { execFileSync } from "node:child_process";
import { mkdirSync, readFileSync, rmSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const desktop = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const repo = resolve(desktop, "..");
const outDir = join(desktop, "bin");
const universal = process.argv.includes("--universal");
const version = JSON.parse(readFileSync(join(desktop, "package.json"), "utf8")).version;

function gitDescribe() {
  try {
    return execFileSync("git", ["describe", "--tags", "--always", "--dirty"], { cwd: repo, encoding: "utf8" }).trim();
  } catch {
    return `desktop-${version}`;
  }
}

const ldflags = `-s -w -X github.com/abdul-hamid-achik/monitor/internal/cli.Version=${gitDescribe()}`;

/** @param {string} goarch @param {string} out */
function build(goarch, out) {
  execFileSync("go", ["build", "-trimpath", "-ldflags", ldflags, "-o", out, "./cmd/monitor"], {
    cwd: repo,
    stdio: "inherit",
    env: { ...process.env, CGO_ENABLED: "0", GOOS: process.platform === "darwin" ? "darwin" : process.env.GOOS ?? "linux", GOARCH: goarch },
  });
}

mkdirSync(outDir, { recursive: true });
const target = join(outDir, "monitor");
// go build -o refuses to overwrite a file it does not recognize as its own
// output, and a universal (lipo) binary from an earlier release build is one.
rmSync(target, { force: true });
if (universal && process.platform === "darwin") {
  const arm = join(outDir, "monitor-arm64");
  const amd = join(outDir, "monitor-amd64");
  build("arm64", arm);
  build("amd64", amd);
  execFileSync("lipo", ["-create", "-output", target, arm, amd], { stdio: "inherit" });
  rmSync(arm);
  rmSync(amd);
} else {
  build(process.arch === "arm64" ? "arm64" : "amd64", target);
}
console.log(`built ${target}`);
