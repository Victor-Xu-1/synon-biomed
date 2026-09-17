#!/usr/bin/env node

import { createHash } from "node:crypto"
import { readFileSync, readdirSync, statSync } from "node:fs"
import { basename, join, relative, resolve } from "node:path"
import { pathToFileURL } from "node:url"

const acornPath = process.env.SYNON_ACORN_MODULE ||
  resolve(import.meta.dirname, "../../frontend/node_modules/acorn/dist/acorn.mjs")
const { parse } = await import(pathToFileURL(acornPath).href)

const [bundleArgument, releaseArgument] = process.argv.slice(2)
if (!bundleArgument || !releaseArgument) {
  console.error("usage: reference_harness_inventory.mjs <bundle.js> <release-dir>")
  process.exit(2)
}

const bundlePath = resolve(bundleArgument)
const releaseRoot = resolve(releaseArgument)
const source = readFileSync(bundlePath, "utf8")
const syntax = parse(source, {
  ecmaVersion: "latest",
  sourceType: "module",
  allowHashBang: true,
})

function propertyName(property) {
  if (!property || property.type !== "Property" || property.computed) return ""
  if (property.key?.type === "Identifier") return property.key.name
  if (property.key?.type === "Literal" && typeof property.key.value === "string") return property.key.value
  return ""
}

function literalString(node) {
  return node?.type === "Literal" && typeof node.value === "string" ? node.value : ""
}

const modelTools = new Set()
const routeLiterals = new Set()
const hostMethods = new Set()
const environmentVariables = new Set()
const bindings = new Map()
const stack = [syntax]
while (stack.length > 0) {
  const node = stack.pop()
  if (!node || typeof node !== "object") continue
  if (node.type === "ObjectExpression") {
    const properties = new Map(node.properties.map((property) => [propertyName(property), property]))
    const name = literalString(properties.get("name")?.value)
    if (name && (properties.has("input_schema") || (properties.has("parameters") && properties.has("handler")))) {
      modelTools.add(name)
    }
  }
  if (node.type === "AssignmentExpression" && node.operator === "=" && node.left?.type === "Identifier") {
    bindings.set(node.left.name, node.right)
  }
  if (node.type === "VariableDeclarator" && node.id?.type === "Identifier" && node.init) {
    bindings.set(node.id.name, node.init)
  }
  if (node.type === "Literal" && typeof node.value === "string") {
    const value = node.value
    if (value.startsWith("/api/")) routeLiterals.add(value)
    for (const match of value.matchAll(/\bhost\.[a-z][a-z0-9_.]*/g)) hostMethods.add(match[0])
    for (const match of value.matchAll(/\bOPERON_[A-Z0-9_]+\b/g)) environmentVariables.add(match[0])
  }
  for (const [key, value] of Object.entries(node)) {
    if (key === "start" || key === "end" || key === "loc") continue
    if (Array.isArray(value)) {
      for (let index = value.length - 1; index >= 0; index -= 1) stack.push(value[index])
    } else if (value && typeof value === "object" && typeof value.type === "string") {
      stack.push(value)
    }
  }
}

function resolveBoundString(node, seen = new Set()) {
  const literal = literalString(node)
  if (literal) return literal
  if (node?.type !== "Identifier" || seen.has(node.name)) return ""
  seen.add(node.name)
  return resolveBoundString(bindings.get(node.name), seen)
}

function resolveToolNames(node, seen = new Set()) {
  if (!node || seen.has(node)) return []
  seen.add(node)
  if (node.type === "Identifier") return resolveToolNames(bindings.get(node.name), seen)
  if (node.type === "SpreadElement") return resolveToolNames(node.argument, seen)
  if (node.type === "CallExpression" && node.callee?.type === "Identifier" && node.callee.name === "$iW") {
    return ["bash"]
  }
  if (node.type === "ArrayExpression") return node.elements.flatMap((item) => resolveToolNames(item, new Set(seen)))
  if (node.type === "ObjectExpression") {
    const name = node.properties.find((property) => propertyName(property) === "name")
    const value = resolveBoundString(name?.value)
    return value ? [value] : []
  }
  return []
}

const coreToolBindings = ["Vw7", "_$_"]
const brainToolBindings = ["Iw7", "Sw7", "kw7", "vw7", "Tw7", "Aw7", "Rw7", "Pw7", "gw7", "bw7", "hw7", "Dw7"]
const operonCoreTools = [...new Set(coreToolBindings.flatMap((name) => resolveToolNames(bindings.get(name))))].sort()
const operonBrainTools = [...new Set(brainToolBindings.flatMap((name) => resolveToolNames(bindings.get(name))))].sort()
const operonHarnessTools = [...new Set([
  ...operonCoreTools,
  ...operonBrainTools,
  "skill",
  "update_step_status",
  "web_fetch",
])].sort()

for (const internalName of [
  "classify_safety",
  "classify_trajectory",
  "compute_attach",
  "compute_close",
  "compute_config_get",
  "compute_ledger",
  "compute_lookup_target",
  "compute_status",
  "powershell",
]) {
  modelTools.delete(internalName)
}
if (source.includes("repl control-plane")) modelTools.add("repl")
if (source.includes("web_search_20250305")) modelTools.add("web_search")
if (source.includes("web_fetch_20260209")) modelTools.add("web_fetch")

function filesUnder(root) {
  const output = []
  const pending = [root]
  while (pending.length > 0) {
    const directory = pending.pop()
    for (const entry of readdirSync(directory, { withFileTypes: true })) {
      const path = join(directory, entry.name)
      if (entry.isDirectory()) pending.push(path)
      else if (entry.isFile()) output.push(relative(root, path).replaceAll("\\", "/"))
    }
  }
  return output.sort()
}

const releaseFiles = filesUnder(releaseRoot)
const skills = [...new Set(
  releaseFiles
    .filter((path) => /^skills\/[^/]+\/SKILL\.md$/.test(path))
    .map((path) => path.split("/")[1]),
)].sort()
const agents = [...new Set(releaseFiles.filter((path) => path.startsWith("agents/")).map((path) => path.split("/")[1]))].sort()
const tables = new Set()
for (const path of releaseFiles.filter((value) => value.startsWith("drizzle/sqlite/") && value.endsWith(".sql"))) {
  const sql = readFileSync(join(releaseRoot, path), "utf8")
  for (const match of sql.matchAll(/CREATE\s+TABLE(?:\s+IF\s+NOT\s+EXISTS)?\s+[`"]?([A-Za-z_][A-Za-z0-9_]*)/gi)) {
    if (!match[1].startsWith("__new_")) tables.add(match[1].toLowerCase())
  }
  for (const match of sql.matchAll(/ALTER\s+TABLE\s+[`"]?([A-Za-z_][A-Za-z0-9_]*)[`"]?\s+RENAME\s+TO\s+[`"]?([A-Za-z_][A-Za-z0-9_]*)/gi)) {
    tables.delete(match[1].toLowerCase())
    if (!match[2].startsWith("__new_")) tables.add(match[2].toLowerCase())
  }
  for (const match of sql.matchAll(/DROP\s+TABLE(?:\s+IF\s+EXISTS)?\s+[`"]?([A-Za-z_][A-Za-z0-9_]*)/gi)) {
    tables.delete(match[1].toLowerCase())
  }
}

const report = {
  schemaVersion: 1,
  reference: {
    release: basename(releaseRoot),
    bundleBytes: statSync(bundlePath).size,
    bundleSHA256: createHash("sha256").update(source).digest("hex"),
  },
  modelTools: [...modelTools].sort(),
  operonCoreTools,
  operonBrainTools,
  operonHarnessTools,
  hostMethods: [...hostMethods].sort(),
  routeLiterals: [...routeLiterals].sort(),
  environmentVariables: [...environmentVariables].sort(),
  skills,
  agents,
  tables: [...tables].sort(),
  releaseFiles,
}
process.stdout.write(`${JSON.stringify(report, null, 2)}\n`)
