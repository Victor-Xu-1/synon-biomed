import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { mkdtemp, mkdir, readFile, readdir, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawnSync } from 'node:child_process';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const script = fileURLToPath(new URL('./generate-conda-runtime-lock.mjs', import.meta.url));

const metadata = (name, version, build, sha256, overrides = {}) => ({
  name,
  version,
  build,
  build_number: 0,
  subdir: 'linux-64',
  channel: 'conda-forge',
  url: `https://conda.anaconda.org/conda-forge/linux-64/${name}-${version}-${build}.conda`,
  sha256,
  license: 'BSD-3-Clause',
  depends: [],
  ...overrides,
});

const runGenerator = (prefix, outputDirectory, extra = [], name = 'synon-biomed-python-baseline') =>
  spawnSync(
    process.execPath,
    [
      script,
      '--prefix',
      prefix,
      '--output-dir',
      outputDirectory,
      '--catalog',
      join(outputDirectory, '..', 'manifest.json'),
      '--name',
      name,
      ...extra,
    ],
    { encoding: 'utf8' },
  );

test('generates deterministic manifest and explicit lock from verified metadata', async () => {
  const root = await mkdtemp(join(tmpdir(), 'synon-conda-lock-'));
  try {
    const metadataRoot = join(root, 'prefix', 'conda-meta');
    await mkdir(metadataRoot, { recursive: true });
    await writeFile(
      join(metadataRoot, 'zlib.json'),
      JSON.stringify(
        metadata('zlib', '1.3.1', 'h4ab18f5_2', 'b'.repeat(64), {
          subdir: 'noarch',
          url: 'https://conda.anaconda.org/conda-forge/noarch/zlib-1.3.1-h4ab18f5_2.conda',
        }),
      ),
    );
    await writeFile(
      join(metadataRoot, 'python.json'),
      JSON.stringify(metadata('python', '3.11.15', 'h3c07f61_0_cpython', 'a'.repeat(64))),
    );
    const outputDirectory = join(root, 'locks');
    const result = runGenerator(join(root, 'prefix'), outputDirectory);
    assert.equal(result.status, 0, result.stderr);

    const generation = result.stdout.match(/sha256=([a-f0-9]{64})/u)?.[1];
    assert.ok(generation);
    const generationDirectory = join(outputDirectory, generation);
    const envelope = JSON.parse(await readFile(join(generationDirectory, 'manifest.json'), 'utf8'));
    assert.deepEqual(
      envelope.packages.map((item) => item.name),
      ['python', 'zlib'],
    );
    const { manifestSHA256, ...manifest } = envelope;
    const body = `${JSON.stringify(manifest, null, 2)}\n`;
    assert.equal(manifestSHA256, createHash('sha256').update(body).digest('hex'));

    const explicit = await readFile(join(generationDirectory, 'explicit.txt'), 'utf8');
    assert.match(explicit, /^# Generated from verified Conda package metadata/mu);
    assert.match(explicit, /@EXPLICIT\nhttps:\/\/conda\.anaconda\.org/mu);
    assert.ok(explicit.indexOf('/python-') < explicit.indexOf('/zlib-'));
    assert.equal(envelope.explicitSHA256, createHash('sha256').update(explicit).digest('hex'));

    const licenses = JSON.parse(await readFile(join(generationDirectory, 'licenses.json'), 'utf8'));
    assert.equal(licenses.source, 'verified-conda-meta-license-fields');
    assert.equal(licenses.licenseTextsIncluded, false);
    assert.equal(licenses.packageCount, 2);
    assert.deepEqual(
      licenses.packages.map((item) => ({ name: item.name, license: item.license })),
      [
        { name: 'python', license: 'BSD-3-Clause' },
        { name: 'zlib', license: 'BSD-3-Clause' },
      ],
    );
    const licenseText = await readFile(join(generationDirectory, 'licenses.json'), 'utf8');
    assert.equal(envelope.licenseInventorySHA256, createHash('sha256').update(licenseText).digest('hex'));

    const catalog = JSON.parse(await readFile(join(outputDirectory, '..', 'manifest.json'), 'utf8'));
    const { catalogSHA256, ...catalogBody } = catalog;
    assert.equal(catalogSHA256, createHash('sha256').update(`${JSON.stringify(catalogBody, null, 2)}\n`).digest('hex'));
    assert.equal(catalog.platform, 'linux-x86_64');
    assert.equal(catalog.schemaVersion, 2);
    assert.equal('oracle' in catalog, false);
    assert.equal(catalog.runtimes.length, 1);
    assert.deepEqual(catalog.runtimes[0], {
      name: 'synon-biomed-python-baseline',
      platform: 'linux-x86_64',
      packageCount: 2,
      generation,
      manifestPath: `locks/${generation}/manifest.json`,
      explicitPath: `locks/${generation}/explicit.txt`,
      licensesPath: `locks/${generation}/licenses.json`,
      licenseTextsIncluded: false,
      explicitSHA256: envelope.explicitSHA256,
      licenseInventorySHA256: envelope.licenseInventorySHA256,
    });

    const repeated = runGenerator(join(root, 'prefix'), outputDirectory);
    assert.equal(repeated.status, 0, repeated.stderr);
    assert.match(repeated.stdout, new RegExp(`sha256=${generation}`, 'u'));
    assert.deepEqual(await readdir(outputDirectory), [generation]);

    const checked = runGenerator(join(root, 'prefix'), outputDirectory, ['--check']);
    assert.equal(checked.status, 0, checked.stderr);
    assert.match(checked.stdout, /mode=check/u);

    await writeFile(join(generationDirectory, 'explicit.txt'), `${explicit}# drift\n`);
    const drifted = runGenerator(join(root, 'prefix'), outputDirectory, ['--check']);
    assert.notEqual(drifted.status, 0);
    assert.match(drifted.stderr, /not generated from the verified metadata/u);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test('reconstructs missing licenses from a verified package cache and enforces required capabilities', async () => {
  const root = await mkdtemp(join(tmpdir(), 'synon-conda-lock-cache-'));
  try {
    const prefix = join(root, 'prefix');
    const metadataRoot = join(prefix, 'conda-meta');
    const packageCache = join(root, 'packages');
    const rdkitIdentity = 'rdkit-2024.03.5-py311h0';
    const rdkitCache = join(packageCache, rdkitIdentity);
    await mkdir(metadataRoot, { recursive: true });
    await mkdir(join(rdkitCache, 'info'), { recursive: true });
    await writeFile(
      join(metadataRoot, 'python.json'),
      JSON.stringify(metadata('python', '3.11.15', 'h0', 'a'.repeat(64))),
    );
    await writeFile(
      join(metadataRoot, 'rdkit.json'),
      JSON.stringify(
        metadata('rdkit', '2024.03.5', 'py311h0', 'b'.repeat(64), {
          license: '',
          extracted_package_dir: rdkitCache,
        }),
      ),
    );
    await writeFile(
      join(rdkitCache, 'info', 'index.json'),
      JSON.stringify({ name: 'rdkit', version: '2024.03.5', build: 'py311h0', license: 'BSD-3-Clause' }),
    );

    const outputDirectory = join(root, 'locks', 'synon-biomed-python');
    const result = runGenerator(prefix, outputDirectory, [
      '--package-cache',
      packageCache,
      '--required-packages',
      'python=3.11.*,rdkit=2024.03.5',
    ], 'synon-biomed-python');
    assert.equal(result.status, 0, result.stderr);
    const generation = result.stdout.match(/sha256=([a-f0-9]{64})/u)?.[1];
    const manifest = JSON.parse(await readFile(join(outputDirectory, generation, 'manifest.json'), 'utf8'));
    assert.deepEqual(manifest.requiredPackages, [
      { name: 'python', version: '3.11.*' },
      { name: 'rdkit', version: '2024.03.5' },
    ]);
    const licenses = JSON.parse(await readFile(join(outputDirectory, generation, 'licenses.json'), 'utf8'));
    assert.equal(licenses.source, 'verified-conda-meta-and-package-cache-license-fields');
    assert.equal(licenses.packages.find((item) => item.name === 'rdkit')?.license, 'BSD-3-Clause');

    const wrongVersion = runGenerator(prefix, join(root, 'wrong'), [
      '--package-cache',
      packageCache,
      '--required-packages',
      'rdkit=2025.03.1',
    ]);
    assert.notEqual(wrongVersion.status, 0);
    assert.match(wrongVersion.stderr, /required package rdkit=2025\.03\.1/u);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test('merges stable runtime entries into one root catalog', async () => {
  const root = await mkdtemp(join(tmpdir(), 'synon-conda-lock-catalog-'));
  try {
    for (const [runtime, packageName] of [
      ['python', 'python'],
      ['r', 'r-base'],
    ]) {
      const metadataRoot = join(root, runtime, 'conda-meta');
      await mkdir(metadataRoot, { recursive: true });
      await writeFile(
        join(metadataRoot, `${packageName}.json`),
        JSON.stringify(metadata(packageName, '1.0.0', 'h0', runtime === 'python' ? 'a'.repeat(64) : 'b'.repeat(64))),
      );
      const result = runGenerator(
        join(root, runtime),
        join(root, 'locks', runtime),
        [],
        `synon-biomed-${runtime}`,
      );
      assert.equal(result.status, 0, result.stderr);
    }
    const catalog = JSON.parse(await readFile(join(root, 'locks', 'manifest.json'), 'utf8'));
    assert.deepEqual(
      catalog.runtimes.map((runtime) => runtime.name),
      ['synon-biomed-python', 'synon-biomed-r'],
    );
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test('rejects unsupported catalog metadata even with a recomputed digest', async () => {
  const root = await mkdtemp(join(tmpdir(), 'synon-conda-lock-schema-'));
  try {
    const prefix = join(root, 'prefix');
    await mkdir(join(prefix, 'conda-meta'), { recursive: true });
    await writeFile(join(prefix, 'conda-meta', 'python.json'),
      JSON.stringify(metadata('python', '3.11.15', 'h0', 'a'.repeat(64))));
    const outputDirectory = join(root, 'locks');
    const generated = runGenerator(prefix, outputDirectory);
    assert.equal(generated.status, 0, generated.stderr);
    const catalogPath = join(root, 'manifest.json');
    const catalog = JSON.parse(await readFile(catalogPath, 'utf8'));
    const { catalogSHA256, ...body } = catalog;
    body.unrecognizedMetadata = { label: 'not-part-of-contract' };
    body.catalogSHA256 = createHash('sha256').update(`${JSON.stringify(body, null, 2)}\n`).digest('hex');
    await writeFile(catalogPath, `${JSON.stringify(body, null, 2)}\n`);
    const rejected = runGenerator(prefix, outputDirectory);
    assert.notEqual(rejected.status, 0);
    assert.match(rejected.stderr, /catalog contract does not match/u);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test('accepts the audited Conda metadata size range', async () => {
  const root = await mkdtemp(join(tmpdir(), 'synon-conda-lock-large-metadata-'));
  try {
    const metadataRoot = join(root, 'prefix', 'conda-meta');
    await mkdir(metadataRoot, { recursive: true });
    await writeFile(
      join(metadataRoot, 'qt6-main.json'),
      JSON.stringify(
        metadata('qt6-main', '6.11.1', 'pl5321h16c4a6b_1', 'c'.repeat(64), {
          sourceMetadataPadding: 'x'.repeat(5 * 1024 * 1024),
        }),
      ),
    );
    const outputDirectory = join(root, 'locks');
    const result = runGenerator(join(root, 'prefix'), outputDirectory);
    assert.equal(result.status, 0, result.stderr);
    const generation = result.stdout.match(/sha256=([a-f0-9]{64})/u)?.[1];
    assert.ok(generation);
    const envelope = JSON.parse(await readFile(join(outputDirectory, generation, 'manifest.json'), 'utf8'));
    assert.equal(envelope.packageCount, 1);
    assert.equal(envelope.packages[0].name, 'qt6-main');
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test('rejects untrusted metadata and unknown or duplicate options', async () => {
  const root = await mkdtemp(join(tmpdir(), 'synon-conda-lock-invalid-'));
  try {
    const metadataRoot = join(root, 'prefix', 'conda-meta');
    await mkdir(metadataRoot, { recursive: true });
    await writeFile(
      join(metadataRoot, 'python.json'),
      JSON.stringify(
        metadata('python', '3.11.15', 'h3c07f61_0_cpython', 'a'.repeat(64), {
          url: 'http://example.invalid/python.conda',
        }),
      ),
    );
    const outputDirectory = join(root, 'locks');
    const invalidMetadata = runGenerator(join(root, 'prefix'), outputDirectory);
    assert.notEqual(invalidMetadata.status, 0);
    assert.match(invalidMetadata.stderr, /credential-free trusted HTTPS URL/u);

    await writeFile(
      join(metadataRoot, 'python.json'),
      JSON.stringify(
        metadata('python', '3.11.15', 'h3c07f61_0_cpython', 'a'.repeat(64), {
          url: 'https://secret@conda.anaconda.org/conda-forge/linux-64/python.conda?token=secret',
        }),
      ),
    );
    const credentialURL = runGenerator(join(root, 'prefix'), outputDirectory);
    assert.notEqual(credentialURL.status, 0);
    assert.match(credentialURL.stderr, /credential-free trusted HTTPS URL/u);

    await writeFile(
      join(metadataRoot, 'python.json'),
      JSON.stringify(
        metadata('python', '3.11.15', 'h3c07f61_0_cpython', 'a'.repeat(64), {
          subdir: 'win-64',
          url: 'https://conda.anaconda.org/conda-forge/win-64/python.conda',
        }),
      ),
    );
    const wrongPlatform = runGenerator(join(root, 'prefix'), outputDirectory);
    assert.notEqual(wrongPlatform.status, 0);
    assert.match(wrongPlatform.stderr, /not supported for linux-x86_64/u);

    const unknown = runGenerator(join(root, 'prefix'), outputDirectory, ['--unexpected', 'value']);
    assert.notEqual(unknown.status, 0);
    assert.match(unknown.stderr, /unknown option/u);

    const duplicate = runGenerator(join(root, 'prefix'), outputDirectory, ['--name', 'duplicate']);
    assert.notEqual(duplicate.status, 0);
    assert.match(duplicate.stderr, /duplicate option/u);

    await writeFile(
      join(metadataRoot, 'python.json'),
      JSON.stringify(metadata('python', '3.11.15', 'h3c07f61_0_cpython', 'a'.repeat(64))),
    );
    const invalidReferenceMode = runGenerator(join(root, 'prefix'), outputDirectory, ['--reference-mode', 'exact-clone']);
    assert.notEqual(invalidReferenceMode.status, 0);
    assert.match(invalidReferenceMode.stderr, /unknown option --reference-mode/u);

    await writeFile(
      join(metadataRoot, 'python.json'),
      JSON.stringify(metadata('python', '3.11.15', 'h3c07f61_0_cpython', 'a'.repeat(64), { license: '' })),
    );
    const missingLicense = runGenerator(join(root, 'prefix'), outputDirectory);
    assert.notEqual(missingLicense.status, 0);
    assert.match(missingLicense.stderr, /license must be a bounded non-empty string/u);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});
