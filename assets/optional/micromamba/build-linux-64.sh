#!/usr/bin/env bash
# Build-only recipe. Never invoked by task execution or environment recovery.
set -euo pipefail
asset_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
command -v patchelf >/dev/null
if [[ $(uname -s) != Linux || $(uname -m) != x86_64 ]]; then
  echo 'This recipe requires Linux x86_64.' >&2
  exit 1
fi
if [[ $# != 1 || $1 != /* || -e $1 ]]; then
  echo 'Usage: bash build-linux-64.sh /absolute/new/external/build-directory' >&2
  exit 2
fi
build_root=$1
product_root=$(cd -- "$asset_dir/../../.." && pwd -P)
case "$(realpath -m -- "$build_root")/" in
  "$product_root/"*) echo 'Build directory must be outside the product repository.' >&2; exit 2 ;;
esac
mkdir -p -- "$build_root"
build_root=$(cd -- "$build_root" && pwd -P)
mkdir -p -- "$build_root/bootstrap"
# Upstream activation requires the executable basename to remain micromamba.
bootstrap="$build_root/bootstrap/micromamba"
if [[ -n ${SYNON_INSTALLER_BOOTSTRAP:-} ]]; then
  install -m 700 -- "$SYNON_INSTALLER_BOOTSTRAP" "$bootstrap"
else
  curl --silent --show-error --fail --location --retry 3 --connect-timeout 20 --max-time 600 \
    https://github.com/mamba-org/micromamba-releases/releases/download/2.9.0-0/micromamba-linux-64 \
    --output "$bootstrap"
fi
printf '366cd9cd8be14df1ab8ed50352a82111082a36686b2d389fdb79a92c3fafb3e3  %s\n' "$bootstrap" | sha256sum -c -
chmod 700 "$bootstrap"
git clone --depth 1 --branch 2.9.0 https://github.com/mamba-org/mamba.git "$build_root/source"
test "$(git -C "$build_root/source" rev-parse HEAD)" = 2676ec2050f7dd5b8a524287526f50a8a4fb9652
git -C "$build_root/source" apply --check "$asset_dir/link-script-exit.patch"
git -C "$build_root/source" apply "$asset_dir/link-script-exit.patch"
export MAMBA_ROOT_PREFIX="$build_root/cache"
export MAMBA_DOWNLOAD_THREADS=4 MAMBA_EXTRACT_THREADS=4
"$bootstrap" --no-rc create -y -p "$build_root/environment" -f "$asset_dir/build-linux-64.lock"
"$bootstrap" run -p "$build_root/environment" bash -c '
  set -euo pipefail
  build_root=$1
  test "$CC" = "$build_root/environment/bin/x86_64-conda-linux-gnu-cc"
  test "$CXX" = "$build_root/environment/bin/x86_64-conda-linux-gnu-c++"
  cmake -S "$build_root/source" -B "$build_root/build" -G Ninja \
    -DCMAKE_PREFIX_PATH="$build_root/environment" -DCMAKE_INSTALL_PREFIX="$build_root/environment" \
    -DCMAKE_BUILD_TYPE=Release -DBUILD_LIBMAMBA=ON -DBUILD_LIBMAMBA_SPDLOG=ON \
    -DBUILD_MICROMAMBA=ON -DMAMBA_LTO=OFF -DCMAKE_SKIP_RPATH=ON \
    "-DCMAKE_C_FLAGS=$CFLAGS -ffile-prefix-map=$build_root=/usr/src/synon-managed-installer" \
    "-DCMAKE_CXX_FLAGS=$CXXFLAGS -ffile-prefix-map=$build_root=/usr/src/synon-managed-installer"
' bash "$build_root"
"$bootstrap" run -p "$build_root/environment" cmake --build "$build_root/build" --target micromamba --parallel 3
"$build_root/environment/bin/x86_64-conda-linux-gnu-strip" --strip-unneeded \
  -o "$build_root/micromamba" "$build_root/build/micromamba/micromamba"
# Conda compiler specs inject a build-environment RPATH even for this static
# library closure. Runtime resolution must use only the platform loader.
patchelf --remove-rpath "$build_root/micromamba"
"$build_root/micromamba" --version
"$build_root/environment/bin/python" "$asset_dir/collect-build-notices.py" \
  "$build_root/environment" "$build_root/cache" "$build_root/DEPENDENCY-NOTICES.txt"
sha256sum "$build_root/micromamba"
printf 'Built candidate: %s\n' "$build_root/micromamba"
