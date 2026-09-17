#!/usr/bin/env node

import { createHash, randomUUID } from 'node:crypto';
import { mkdir, readdir, readFile, realpath, rename, rm, stat, writeFile } from 'node:fs/promises';
import { basename, dirname, join, relative, resolve, sep } from 'node:path';

const MAX_METADATA_FILES = 4096;
// Verified Conda records can exceed 5 MB (notably qt6-main).
// Keep the bound above that audited metadata maximum while remaining
// small enough to reject unexpectedly large or hostile metadata files.
const MAX_METADATA_BYTES = 8 * 1024 * 1024;
const MAX_DEPENDENCIES = 4096;
const VALUE_OPTION_NAMES = new Set([
  'prefix',
  'output-dir',
  'catalog',
  'name',
  'package-cache',
  'required-packages',
]);
const FLAG_OPTION_NAMES = new Set(['check']);
const ALLOWED_SUBDIRS = new Set(['linux-64', 'noarch']);
const ALLOWED_PACKAGE_HOSTS = new Set(['conda.anaconda.org']);

const fail = (message) => {
  process.stderr.write(`${message}\n`);
  process.exitCode = 1;
};

const parseArguments = (values) => {
  const options = {};
  for (let index = 0; index < values.length; index += 1) {
    const key = values[index];
    if (!key?.startsWith('--')) {
      throw new Error('arguments must use named --options');
    }
    const name = key.slice(2);
    if (!VALUE_OPTION_NAMES.has(name) && !FLAG_OPTION_NAMES.has(name)) {
      throw new Error(`unknown option --${name}`);
    }
    if (options[name] !== undefined) {
      throw new Error(`duplicate option --${name}`);
    }
    if (FLAG_OPTION_NAMES.has(name)) {
      options[name] = true;
      continue;
    }
    const value = values[index + 1];
    if (!value || value.startsWith('--')) {
      throw new Error(`--${name} requires a value`);
    }
    options[name] = value;
    index += 1;
  }
  for (const required of ['prefix', 'output-dir', 'catalog', 'name']) {
    if (!options[required]?.trim()) {
      throw new Error(`--${required} is required`);
    }
  }
  return options;
};

const boundedText = (value, field, maximum = 4096) => {
  if (typeof value !== 'string' || value.length === 0 || value.length > maximum || /[\0\r\n]/u.test(value)) {
    throw new Error(`${field} must be a bounded non-empty string`);
  }
  return value;
};

const canonicalJSON = (value) => `${JSON.stringify(value, null, 2)}\n`;

const sha256 = (value) => createHash('sha256').update(value).digest('hex');

const relativeAssetPath = (catalogPath, assetPath, field) => {
  const value = relative(dirname(catalogPath), assetPath);
  if (!value || value === '..' || value.startsWith(`..${sep}`) || value.startsWith('/') || value.includes('\\')) {
    throw new Error(`${field} must stay under the catalog directory`);
  }
  return value.split(sep).join('/');
};

const pathStaysUnder = (root, candidate) => {
  const value = relative(root, candidate);
  return value !== '..' && !value.startsWith(`..${sep}`) && !value.startsWith('/') && !value.includes('\\');
};

const cachedPackageLicense = async (decoded, entry, packageCache, identity) => {
  if (!packageCache) {
    throw new Error(
      `${entry}.license must be a bounded non-empty string; --package-cache is required for explicit-lock environments`,
    );
  }
  const configured = boundedText(decoded.extracted_package_dir, `${entry}.extracted_package_dir`, 4096);
  const extracted = await realpath(resolve(configured));
  if (!pathStaysUnder(packageCache, extracted) || basename(extracted) !== identity) {
    throw new Error(`${entry}.extracted_package_dir is outside the verified package cache`);
  }
  const indexPath = join(extracted, 'info', 'index.json');
  const indexStat = await stat(indexPath);
  if (!indexStat.isFile() || indexStat.size <= 0 || indexStat.size > MAX_METADATA_BYTES) {
    throw new Error(`${entry} cached package index is invalid`);
  }
  const index = JSON.parse(await readFile(indexPath, 'utf8'));
  if (index?.name !== decoded.name || index?.version !== decoded.version || index?.build !== decoded.build) {
    throw new Error(`${entry} cached package index does not match the installed package identity`);
  }
  return boundedText(typeof index.license === 'string' ? index.license.trim() : '', `${entry}.license`, 1024);
};

const requiredPackages = (value) => {
  if (!value) return [];
  const seen = new Set();
  const requirements = boundedText(value, '--required-packages', 4096)
    .split(',')
    .map((item) => item.trim())
    .filter(Boolean)
    .map((item) => {
      const match = /^([A-Za-z0-9][A-Za-z0-9._-]{0,127})=([A-Za-z0-9][A-Za-z0-9._+*-]{0,127})$/u.exec(item);
      if (!match) throw new Error(`invalid required package ${item}`);
      if (seen.has(match[1])) throw new Error(`duplicate required package ${match[1]}`);
      seen.add(match[1]);
      return { name: match[1], version: match[2] };
    });
  return requirements.sort((left, right) => left.name.localeCompare(right.name));
};

const packageVersionMatches = (actual, required) =>
  required.endsWith('.*') ? actual.startsWith(required.slice(0, -1)) : actual === required;

const trustedPackageURL = (value, field) => {
  const text = boundedText(value, field, 4096);
  let parsed;
  try {
    parsed = new URL(text);
  } catch {
    throw new Error(`${field} is not a valid URL`);
  }
  if (
    parsed.protocol !== 'https:' ||
    parsed.username ||
    parsed.password ||
    parsed.search ||
    parsed.hash ||
    !ALLOWED_PACKAGE_HOSTS.has(parsed.hostname) ||
    /%2f|%5c/iu.test(parsed.pathname) ||
    parsed.pathname.includes('\\')
  ) {
    throw new Error(`${field} must be a credential-free trusted HTTPS URL`);
  }
  return parsed;
};

const trustedChannel = (value, field) => {
  const text = boundedText(value, field, 512);
  if (/^[a-z0-9][a-z0-9._-]{0,127}$/u.test(text)) {
    return {
      value: text,
      origin: 'https://conda.anaconda.org',
      pathname: `/${text}`,
    };
  }
  const parsed = trustedPackageURL(text, field);
  const pathname = parsed.pathname.replace(/\/+$/u, '');
  if (!pathname) {
    throw new Error(`${field} must identify a trusted channel path`);
  }
  return {
    value: parsed.href.replace(/\/$/u, ''),
    origin: parsed.origin,
    pathname,
  };
};

const readExisting = async (path, field) => {
  try {
    return await readFile(path, 'utf8');
  } catch (error) {
    if (error?.code === 'ENOENT') {
      throw new Error(`${field} is missing`);
    }
    throw error;
  }
};

const writeAtomicFile = async (path, content) => {
  await mkdir(dirname(path), { recursive: true, mode: 0o755 });
  const temporaryPath = `${path}.tmp-${process.pid}-${randomUUID()}`;
  try {
    await writeFile(temporaryPath, content, { encoding: 'utf8', flag: 'wx', mode: 0o644 });
    await rename(temporaryPath, path);
  } finally {
    await rm(temporaryPath, { force: true });
  }
};

const writeOutputs = async (outputDirectory, generation, files) => {
  await mkdir(outputDirectory, { recursive: true, mode: 0o755 });
  const finalDirectory = join(outputDirectory, generation);
  const temporaryDirectory = join(outputDirectory, `.tmp-${process.pid}-${randomUUID()}`);
  try {
    await mkdir(temporaryDirectory, { mode: 0o700 });
    for (const [name, content] of Object.entries(files)) {
      await writeFile(join(temporaryDirectory, name), content, {
        encoding: 'utf8',
        flag: 'wx',
        mode: 0o644,
      });
    }
    await rename(temporaryDirectory, finalDirectory);
  } catch (error) {
    await rm(temporaryDirectory, { recursive: true, force: true });
    if (error?.code !== 'EEXIST' && error?.code !== 'ENOTEMPTY') {
      throw error;
    }
    for (const [name, content] of Object.entries(files)) {
      const existing = await readExisting(join(finalDirectory, name), `existing generation ${generation}/${name}`);
      if (existing !== content) {
        throw new Error(`existing generation ${generation}/${name} does not match the verified lock`);
      }
    }
  }
  return finalDirectory;
};

const verifyOutputs = async (outputDirectory, generation, files) => {
  const generationDirectory = join(outputDirectory, generation);
  for (const [name, content] of Object.entries(files)) {
    const existing = await readExisting(join(generationDirectory, name), `${generation}/${name}`);
    if (existing !== content) {
      throw new Error(`${generation}/${name} is not generated from the verified metadata`);
    }
  }
  const entries = (await readdir(generationDirectory)).sort();
  const expected = Object.keys(files).sort();
  if (JSON.stringify(entries) !== JSON.stringify(expected)) {
    throw new Error(`${generation} contains unexpected generated files`);
  }
  return generationDirectory;
};

const readCatalog = async (catalogPath) => {
  try {
    const decoded = JSON.parse(await readFile(catalogPath, 'utf8'));
    const { catalogSHA256, ...body } = decoded ?? {};
    if (
      body?.schemaVersion !== 2 ||
      body?.platform !== 'linux-x86_64' ||
      Object.keys(body).some((key) => !['schemaVersion', 'platform', 'runtimes'].includes(key)) ||
      !Array.isArray(body.runtimes) ||
      !/^[a-f0-9]{64}$/u.test(catalogSHA256 ?? '') ||
      sha256(canonicalJSON(body)) !== catalogSHA256
    ) {
      throw new Error('catalog contract does not match this generator invocation');
    }
    const names = new Set();
    for (const runtime of body.runtimes) {
      const name = boundedText(runtime?.name, 'catalog runtime name', 128);
      if (names.has(name)) {
        throw new Error(`catalog contains duplicate runtime ${name}`);
      }
      names.add(name);
    }
    return body;
  } catch (error) {
    if (error?.code === 'ENOENT') {
      return { schemaVersion: 2, platform: 'linux-x86_64', runtimes: [] };
    }
    throw error;
  }
};

const main = async () => {
  const options = parseArguments(process.argv.slice(2));
  const prefix = resolve(options.prefix);
  const packageCache = options['package-cache'] ? await realpath(resolve(options['package-cache'])) : '';
  if (packageCache && !(await stat(packageCache)).isDirectory()) {
    throw new Error('--package-cache must be a directory');
  }
  const metadataRoot = join(prefix, 'conda-meta');
  const entries = (await readdir(metadataRoot, { withFileTypes: true }))
    .filter((entry) => entry.isFile() && entry.name.endsWith('.json'))
    .map((entry) => entry.name)
    .sort();
  if (entries.length === 0 || entries.length > MAX_METADATA_FILES) {
    throw new Error(`Conda metadata count ${entries.length} is outside the supported range`);
  }

  const packages = [];
  let usedPackageCacheLicense = false;
  const identities = new Set();
  for (const entry of entries) {
    const metadataPath = join(metadataRoot, entry);
    const metadataStat = await stat(metadataPath);
    if (!metadataStat.isFile() || metadataStat.size <= 0 || metadataStat.size > MAX_METADATA_BYTES) {
      throw new Error(`${entry} exceeds the supported metadata size`);
    }
    const decoded = JSON.parse(await readFile(metadataPath, 'utf8'));
    const subdir = boundedText(decoded.subdir, `${entry}.subdir`, 64);
    if (!ALLOWED_SUBDIRS.has(subdir)) {
      throw new Error(`${entry}.subdir is not supported for linux-x86_64`);
    }
    const channel = trustedChannel(decoded.channel, `${entry}.channel`);
    const packageURL = trustedPackageURL(decoded.url, `${entry}.url`);
    const expectedPackagePrefix = `${channel.pathname}/${subdir}/`;
    if (
      packageURL.origin !== channel.origin ||
      !packageURL.pathname.startsWith(expectedPackagePrefix) ||
      !/\.(?:conda|tar\.bz2)$/u.test(packageURL.pathname)
    ) {
      throw new Error(`${entry}.url does not match its trusted channel and subdir`);
    }
    if (!Array.isArray(decoded.depends) || decoded.depends.length > MAX_DEPENDENCIES) {
      throw new Error(`${entry}.depends exceeds the supported dependency count`);
    }
    const name = boundedText(decoded.name, `${entry}.name`, 256);
    const version = boundedText(decoded.version, `${entry}.version`, 256);
    const build = boundedText(decoded.build, `${entry}.build`, 256);
    let license = typeof decoded.license === 'string' ? decoded.license.trim() : '';
    if (!license) {
      license = await cachedPackageLicense(decoded, entry, packageCache, `${name}-${version}-${build}`);
      usedPackageCacheLicense = true;
    }
    const item = {
      name,
      version,
      build,
      buildNumber: Number(decoded.build_number ?? 0),
      subdir,
      channel: channel.value,
      url: packageURL.href,
      sha256: boundedText(decoded.sha256, `${entry}.sha256`, 64).toLowerCase(),
      license: boundedText(license, `${entry}.license`, 1024),
      depends: decoded.depends.map((value) => boundedText(value, `${entry}.depends`, 512)),
    };
    if (!/^[a-f0-9]{64}$/u.test(item.sha256)) {
      throw new Error(`${entry} has an invalid SHA-256`);
    }
    if (!Number.isSafeInteger(item.buildNumber)) {
      throw new Error(`${entry}.build_number must be an integer`);
    }
    const identity = `${item.name}\0${item.version}\0${item.build}\0${item.subdir}`;
    if (identities.has(identity)) {
      throw new Error(`duplicate package identity ${item.name} ${item.version} ${item.build}`);
    }
    identities.add(identity);
    packages.push(item);
  }
  packages.sort((left, right) =>
    left.name.localeCompare(right.name) || left.version.localeCompare(right.version) || left.build.localeCompare(right.build)
  );
  const required = requiredPackages(options['required-packages']);
  for (const requirement of required) {
    const installed = packages.find((item) => item.name === requirement.name);
    if (!installed || !packageVersionMatches(installed.version, requirement.version)) {
      throw new Error(
        `required package ${requirement.name}=${requirement.version} is not present at the required version`,
      );
    }
  }

  const runtimeRequirements = required.length > 0 ? { requiredPackages: required } : {};
  const explicit = [
    '# Generated from verified Conda package metadata. DO NOT EDIT.',
    '# platform: linux-x86_64',
    '@EXPLICIT',
    ...packages.map((item) => `${item.url}#${item.sha256}`),
    '',
  ].join('\n');
  const licenseInventory = canonicalJSON({
    schemaVersion: 2,
    name: boundedText(options.name, '--name', 128),
    platform: 'linux-x86_64',
    source: usedPackageCacheLicense
      ? 'verified-conda-meta-and-package-cache-license-fields'
      : 'verified-conda-meta-license-fields',
    licenseTextsIncluded: false,
    packageCount: packages.length,
    packages: packages.map(({ name, version, build, license }) => ({ name, version, build, license })),
  });
  const manifest = {
    schemaVersion: 2,
    name: boundedText(options.name, '--name', 128),
    platform: 'linux-x86_64',
    ...runtimeRequirements,
    packageCount: packages.length,
    explicitSHA256: sha256(explicit),
    licenseInventorySHA256: sha256(licenseInventory),
    packages,
  };
  const manifestBody = canonicalJSON(manifest);
  const manifestSHA256 = sha256(manifestBody);
  const envelope = canonicalJSON({ ...manifest, manifestSHA256 });
  const outputDirectory = resolve(options['output-dir']);
  const catalogPath = resolve(options.catalog);
  relativeAssetPath(catalogPath, outputDirectory, '--output-dir');
  const files = {
    'explicit.txt': explicit,
    'licenses.json': licenseInventory,
    'manifest.json': envelope,
  };
  const generationDirectory = options.check
    ? await verifyOutputs(outputDirectory, manifestSHA256, files)
    : await writeOutputs(outputDirectory, manifestSHA256, files);
  const catalog = await readCatalog(catalogPath);
  const catalogEntry = {
    name: manifest.name,
    platform: manifest.platform,
    ...runtimeRequirements,
    packageCount: packages.length,
    generation: manifestSHA256,
    manifestPath: relativeAssetPath(catalogPath, join(generationDirectory, 'manifest.json'), 'manifest path'),
    explicitPath: relativeAssetPath(catalogPath, join(generationDirectory, 'explicit.txt'), 'explicit path'),
    licensesPath: relativeAssetPath(catalogPath, join(generationDirectory, 'licenses.json'), 'licenses path'),
    licenseTextsIncluded: false,
    explicitSHA256: manifest.explicitSHA256,
    licenseInventorySHA256: manifest.licenseInventorySHA256,
  };
  const runtimes = catalog.runtimes.filter((runtime) => runtime.name !== manifest.name);
  runtimes.push(catalogEntry);
  runtimes.sort((left, right) => left.name.localeCompare(right.name));
  const catalogBody = { ...catalog, runtimes };
  const expectedCatalog = canonicalJSON({ ...catalogBody, catalogSHA256: sha256(canonicalJSON(catalogBody)) });
  if (options.check) {
    const existingCatalog = await readExisting(catalogPath, 'runtime catalog');
    if (existingCatalog !== expectedCatalog) {
      throw new Error('runtime catalog is not generated from the verified metadata');
    }
  } else {
    await writeAtomicFile(catalogPath, expectedCatalog);
  }
  process.stdout.write(
    `${basename(generationDirectory)} packages=${packages.length} sha256=${manifestSHA256} mode=${options.check ? 'check' : 'write'}\n`,
  );
};

main().catch((error) => fail(error instanceof Error ? error.message : String(error)));
