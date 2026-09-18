# Managed installer asset

The Linux x86_64 entrypoint is a controlled build of official Mamba 2.9.0,
commit `2676ec2050f7dd5b8a524287526f50a8a4fb9652`, with one source change:
`link-script-exit.patch` makes a nonzero package link-script exit fail the
transaction, including when process creation itself succeeded. This is not
an upstream release binary. The runtime still calls only this one installer;
it never replays package scripts independently.

`manifest.json` is the asset version/checksum authority. The executable's
upstream version output remains `2.9.0`; the asset version is `2.9.0+synon.1`.
The original BSD-3-Clause license is unchanged. The pinned build dependency
closure includes the compiler and static libraries, not runtime installation
alternatives. Dependency distribution sources and hashes are in
`build-linux-64.lock`; their notices are included in `DEPENDENCY-NOTICES.txt`.

To reproduce the build, from this directory on Linux x86_64 with Git, curl,
patchelf (0.18.0 used for this asset), and standard shell utilities. Use a neutral
external build path: static dependency configuration embeds its installation
prefix even though the binary has no runtime dependency on that prefix.

```sh
bash build-linux-64.sh /tmp/synon-installer-build-2.9.0
```

The directory must not exist and must be outside the source repository. The
recipe verifies the bootstrap binary, source commit, and explicit dependency
hashes, then applies the source change and builds a single native artifact.
Nothing is installed into the product or activated automatically. Keep the
build directory for source and dependency inspection. Build paths can affect
binary metadata, so each accepted artifact has its own manifest checksum.
Source filenames are prefix-mapped and the compiler-injected RPATH is removed.
Acceptance must also run with the build environment unavailable, including a
verified HTTPS package fetch using system trust, before replacing the asset.
An existing official bootstrap download may be supplied through
`SYNON_INSTALLER_BOOTSTRAP`; the same mandatory checksum still applies.
`CONDA_PKGS_DIRS` may point to an existing package cache for offline reuse of
the exact locked distributions. Neither setting changes runtime authority.

Before replacing the asset, run the exact real-installer integration tests:

```sh
SYNON_TEST_REAL_INSTALLER=1 SYNON_TEST_MICROMAMBA=/absolute/build/micromamba \
  go test ./internal/kernel -run '^(TestManagedInstallerRealLinkScriptLifecycle|TestManagedInstallerRealPublicationAndRestartRecovery)$' -count=1
```

Run from the repository root. The tests use local controlled Conda packages,
the real installer, and a real Python interpreter/venv; no scientific task or
network download is required. Verify the asset manifest and executable dynamic
dependencies before packaging. Update the single manifest, dependency notices,
and package assertions together; do not retain the replaced installer as a
runtime fallback.
