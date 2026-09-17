"use strict";

// This boundary confines code generation used by the lockfile-pinned packaged
// RDKit assets away from the page; it is not a sandbox for untrusted code.

const PROTOCOL_VERSION = 1;
const MAX_SMILES_LENGTH = 4096;
const MIN_RENDER_DIMENSION = 16;
const MAX_RENDER_DIMENSION = 1024;
const MAX_SVG_LENGTH = 1024 * 1024;
const MAX_MOLBLOCK_LENGTH = 1024 * 1024;
const REQUEST_KEYS = [
  "height",
  "id",
  "operation",
  "source",
  "version",
  "width",
];

let modulePromise = null;

function respond(message) {
  self.postMessage(message);
}

function validRequest(value) {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const keys = Object.keys(value).sort();
  if (
    keys.length !== REQUEST_KEYS.length ||
    keys.some((key, index) => key !== REQUEST_KEYS[index])
  )
    return false;
  return (
    value.version === PROTOCOL_VERSION &&
    Number.isSafeInteger(value.id) &&
    value.id > 0 &&
    (value.operation === "render_svg" ||
      value.operation === "validate_molblock") &&
    typeof value.source === "string" &&
    value.source.trim().length > 0 &&
    value.source.trim().length <=
      (value.operation === "render_svg"
        ? MAX_SMILES_LENGTH
        : MAX_MOLBLOCK_LENGTH) &&
    Number.isInteger(value.width) &&
    value.width >= MIN_RENDER_DIMENSION &&
    value.width <= MAX_RENDER_DIMENSION &&
    Number.isInteger(value.height) &&
    value.height >= MIN_RENDER_DIMENSION &&
    value.height <= MAX_RENDER_DIMENSION
  );
}

function loadModule() {
  if (modulePromise) return modulePromise;
  modulePromise = Promise.resolve()
    .then(() => {
      const script = new URL("RDKit_minimal.js", self.location.href).toString();
      self.importScripts(script);
      if (typeof self.initRDKitModule !== "function")
        throw new Error("missing RDKit loader");
      return self.initRDKitModule({
        locateFile: (filename) => {
          if (filename !== "RDKit_minimal.wasm")
            throw new Error("unexpected RDKit asset");
          return new URL(filename, self.location.href).toString();
        },
      });
    })
    .catch((error) => {
      modulePromise = null;
      throw error;
    });
  return modulePromise;
}

self.addEventListener("message", async (event) => {
  const request = event.data;
  const id =
    request && Number.isSafeInteger(request.id) && request.id > 0
      ? request.id
      : null;
  if (!validRequest(request)) {
    if (id !== null)
      respond({
        version: PROTOCOL_VERSION,
        id,
        ok: false,
        error: "invalid_request",
      });
    return;
  }

  let rdkit;
  try {
    rdkit = await loadModule();
  } catch {
    respond({
      version: PROTOCOL_VERSION,
      id,
      ok: false,
      error: "runtime_unavailable",
    });
    return;
  }

  let molecule;
  try {
    molecule = rdkit.get_mol(request.source.trim());
  } catch {
    molecule = null;
  }
  if (!molecule) {
    respond({
      version: PROTOCOL_VERSION,
      id,
      ok: false,
      error: "invalid_smiles",
    });
    return;
  }

  try {
    const molBlock = molecule.get_molblock();
    const smiles = molecule.get_smiles();
    if (request.operation === "validate_molblock") molecule.set_new_coords();
    const svg = molecule.get_svg(request.width, request.height);
    if (
      typeof svg !== "string" ||
      svg.length === 0 ||
      svg.length > MAX_SVG_LENGTH
    ) {
      throw new Error("invalid SVG");
    }
    if (
      typeof molBlock !== "string" ||
      molBlock.length > MAX_MOLBLOCK_LENGTH ||
      typeof smiles !== "string"
    ) {
      throw new Error("invalid molecule output");
    }
    respond({ version: PROTOCOL_VERSION, id, ok: true, svg, molBlock, smiles });
  } catch {
    respond({
      version: PROTOCOL_VERSION,
      id,
      ok: false,
      error: "render_failed",
    });
  } finally {
    molecule.delete();
  }
});
