#!/usr/bin/env node
import { execFileSync } from "node:child_process";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { basename } from "node:path";

const projectRoot = process.argv[2] || process.cwd();
const scanPath = `${projectRoot}/.understand-anything/intermediate/scan-script-output.json`;
const importMapPath = `${projectRoot}/.understand-anything/intermediate/import-map.json`;
const structurePath = `${projectRoot}/.understand-anything/intermediate/structure-output.json`;
const outPath = `${projectRoot}/.understand-anything/knowledge-graph.json`;
const metaPath = `${projectRoot}/.understand-anything/meta.json`;

function readJSON(path) {
  return JSON.parse(readFileSync(path, "utf8"));
}

function safeCommit() {
  try {
    return execFileSync("git", ["-C", projectRoot, "rev-parse", "HEAD"], {
      encoding: "utf8",
      stdio: ["ignore", "pipe", "ignore"],
    }).trim();
  } catch {
    return "unknown";
  }
}

function nodeType(file) {
  if (file.fileCategory === "docs") return "document";
  if (file.fileCategory === "config") return "config";
  if (file.fileCategory === "infra") {
    if (file.path.startsWith(".github/workflows/")) return "pipeline";
    return "service";
  }
  if (file.fileCategory === "data") return "resource";
  return "file";
}

function complexity(lines) {
  if (lines > 500) return "complex";
  if (lines > 150) return "moderate";
  return "simple";
}

function topLayer(path) {
  if (!path.includes("/")) return "root";
  const [first, second] = path.split("/");
  if (first === "internal" && second) return `${first}/${second}`;
  return first;
}

function layerName(id) {
  const names = {
    root: "Root / entry docs",
    cmd: "Command entrypoints",
    commands: "Command orchestration",
    "internal/domain": "Domain model",
    "internal/application": "Application services",
    "internal/infrastructure": "Infrastructure adapters",
    "internal/adapter": "External adapters",
    "internal/glossary": "Glossary",
    modules: "Runtime modules",
    config: "Configuration",
    rules: "Agent rules",
    prompts: "Prompts",
    personas: "Personas",
    scripts: "Operational scripts",
    systemd: "Systemd deployment",
    "rencrow-data": "Market data workflow",
    tools: "Repository-local tools",
  };
  return names[id] || id;
}

function layerDescription(id) {
  const descriptions = {
    root: "Top-level documentation, build files, and repository entrypoints.",
    cmd: "Executable command packages and process entrypoints.",
    commands: "Command-level orchestration and user-facing command flows.",
    "internal/domain": "Core domain objects, policies, and business concepts.",
    "internal/application": "Application use cases and service coordination.",
    "internal/infrastructure": "Persistence, external systems, runtime wiring, and platform concerns.",
    "internal/adapter": "Adapters between RenCrow internals and external surfaces.",
    "internal/glossary": "Shared terminology and glossary support.",
    modules: "Feature modules plugged into the runtime.",
    config: "Configuration schemas and examples.",
    rules: "Agent and project rules that constrain behavior.",
    prompts: "Prompt templates and persona instructions.",
    personas: "Character and role definitions.",
    scripts: "Operational and verification scripts.",
    systemd: "User service deployment units and timers.",
    "rencrow-data": "Stock and ETF learning-base data pipeline.",
    tools: "Repository-local helper tools retained for compatibility.",
  };
  return descriptions[id] || `Files under ${id}.`;
}

function briefSymbols(structureByPath, path) {
  const item = structureByPath.get(path);
  if (!item) return "";
  const funcs = (item.functions || []).slice(0, 6).map((f) => f.name);
  const classes = (item.classes || []).slice(0, 6).map((c) => c.name);
  const sections = (item.sections || []).slice(0, 6).map((s) => s.heading);
  const parts = [];
  if (classes.length) parts.push(`types: ${classes.join(", ")}`);
  if (funcs.length) parts.push(`functions: ${funcs.join(", ")}`);
  if (sections.length) parts.push(`sections: ${sections.join(", ")}`);
  return parts.length ? ` Key structure includes ${parts.join("; ")}.` : "";
}

if (!existsSync(scanPath) || !existsSync(importMapPath) || !existsSync(structurePath)) {
  throw new Error("Missing scan/import/structure intermediate files. Run scan, import-map, and extract-structure first.");
}

const scan = readJSON(scanPath);
const importData = readJSON(importMapPath);
const structure = readJSON(structurePath);
const structureByPath = new Map((structure.results || []).map((r) => [r.path, r]));

const nodes = [];
const nodeIds = new Set();
for (const file of scan.files) {
  const type = nodeType(file);
  const id = `${type}:${file.path}`;
  nodeIds.add(id);
  nodes.push({
    id,
    type,
    name: basename(file.path),
    filePath: file.path,
    summary: `${file.fileCategory} file in ${topLayer(file.path)} (${file.language}, ${file.sizeLines} lines).${briefSymbols(structureByPath, file.path)}`,
    tags: [file.language, file.fileCategory, topLayer(file.path)].filter(Boolean),
    complexity: complexity(file.sizeLines),
  });
}

const fileIdByPath = new Map(scan.files.map((file) => [file.path, `${nodeType(file)}:${file.path}`]));
const edges = [];
const seenEdges = new Set();
for (const [sourcePath, targets] of Object.entries(importData.importMap || {})) {
  const source = fileIdByPath.get(sourcePath);
  if (!source) continue;
  for (const targetPath of targets) {
    const target = fileIdByPath.get(targetPath);
    if (!target || source === target) continue;
    const key = `${source}\0${target}\0imports`;
    if (seenEdges.has(key)) continue;
    seenEdges.add(key);
    edges.push({
      source,
      target,
      type: "imports",
      direction: "forward",
      description: `${sourcePath} imports ${targetPath}`,
      weight: 0.7,
    });
  }
}

const layersById = new Map();
for (const file of scan.files) {
  const id = `layer:${topLayer(file.path).replaceAll("/", "-")}`;
  if (!layersById.has(id)) {
    const raw = topLayer(file.path);
    layersById.set(id, {
      id,
      name: layerName(raw),
      description: layerDescription(raw),
      nodeIds: [],
    });
  }
  const nodeId = fileIdByPath.get(file.path);
  if (nodeId) layersById.get(id).nodeIds.push(nodeId);
}

function firstExisting(paths) {
  for (const p of paths) {
    const id = fileIdByPath.get(p);
    if (id && nodeIds.has(id)) return id;
  }
  return null;
}

const tourCandidates = [
  ["Project overview", "Start from the repository overview and agent rules.", ["README.md", "README.ja.md", "AGENTS.md", "CLAUDE.md"]],
  ["Command entrypoints", "See how executable commands enter the runtime.", ["cmd/picoclaw/main.go", "cmd/agent/main.go"]],
  ["Domain model", "Inspect core domain boundaries and policies.", scan.files.filter((f) => f.path.startsWith("internal/domain/")).slice(0, 3).map((f) => f.path)],
  ["Application layer", "Review use-case orchestration and application services.", scan.files.filter((f) => f.path.startsWith("internal/application/")).slice(0, 3).map((f) => f.path)],
  ["Infrastructure layer", "Review persistence, runtime integration, and external system wiring.", scan.files.filter((f) => f.path.startsWith("internal/infrastructure/")).slice(0, 3).map((f) => f.path)],
  ["Runtime modules", "Inspect feature modules attached to the runtime.", scan.files.filter((f) => f.path.startsWith("modules/")).slice(0, 3).map((f) => f.path)],
  ["Operations", "Review deployment and operational scripts.", ["systemd/picoclaw.service", "Makefile", "docker-compose.yml"]],
];

const tour = [];
for (const [title, description, paths] of tourCandidates) {
  const ids = paths.map((p) => firstExisting([p])).filter(Boolean);
  if (!ids.length) continue;
  tour.push({ order: tour.length + 1, title, description, nodeIds: ids });
}

const byLang = scan.stats?.byLanguage || {};
const languages = Object.entries(byLang)
  .sort((a, b) => b[1] - a[1])
  .slice(0, 8)
  .map(([lang]) => lang);

const now = new Date().toISOString();
const graph = {
  version: "1.0.0",
  kind: "codebase",
  project: {
    name: "picoclaw_multiLLM",
    languages,
    frameworks: ["Go", "Node.js", "Python"],
    description: "RenCrow core/chat/CLI runtime, analyzed with deterministic Understand Anything extraction.",
    analyzedAt: now,
    gitCommitHash: safeCommit(),
  },
  nodes,
  edges,
  layers: Array.from(layersById.values()).filter((layer) => layer.nodeIds.length),
  tour,
};

writeFileSync(outPath, JSON.stringify(graph, null, 2));
writeFileSync(metaPath, JSON.stringify({
  lastAnalyzedAt: now,
  gitCommitHash: graph.project.gitCommitHash,
  version: graph.version,
  analyzedFiles: scan.files.length,
  deterministic: true,
}, null, 2));

console.log(JSON.stringify({
  output: outPath,
  meta: metaPath,
  analyzedFiles: scan.files.length,
  nodes: graph.nodes.length,
  edges: graph.edges.length,
  layers: graph.layers.length,
  tourSteps: graph.tour.length,
}, null, 2));
