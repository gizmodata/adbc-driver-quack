#!/usr/bin/env bash
# Build the c-shared ADBC driver library (libadbc_driver_quack.{so,dylib,dll})
# for the target in GOOS/GOARCH (default: this machine).
#
# The library exports only the ADBC entrypoints (Adbc*) plus the deprecated
# QuackDriverInit alias, per the ADBC shared-library requirements
# (https://adbc-drivers.org/building-drivers/shared-library-requirements.html).
# The export lists live next to the package: exports_linux.map (version
# script), exports_darwin.txt (ld64 exported-symbols list) and
# exports_windows.def. On Windows, -Wl,--export-all-symbols makes Go skip its
# own .def file (which lists every //export function, e.g. QuackDatabaseNew)
# and --exclude-all-symbols then turns automatic exporting off again, so the
# DLL exports what the C code marks dllexport (the Adbc* wrappers) plus our
# .def. go run ./scripts/checkexports verifies the result.
#
# Usage: scripts/build_lib.sh [output path]
# CC, CGO_CFLAGS and the like pass through to go build.
set -euo pipefail
cd "$(dirname "$0")/.."

goos=$(go env GOOS)
case "$goos" in
  linux)   ext=so ;;
  darwin)  ext=dylib ;;
  windows) ext=dll ;;
  *) echo "build_lib.sh: unsupported GOOS $goos" >&2; exit 1 ;;
esac
out=${1:-pkg/quack/libadbc_driver_quack.$ext}

# The external linker runs in a temporary directory: give it absolute paths,
# in the native form on Windows (D:/a/..., not Git Bash's /d/a/...).
dir=$PWD/pkg/quack
if command -v cygpath > /dev/null 2>&1; then
  dir=$(cygpath -m "$dir")
fi
case "$goos" in
  linux)   extld="-Wl,--version-script=$dir/exports_linux.map" ;;
  darwin)  extld="-Wl,-exported_symbols_list,$dir/exports_darwin.txt" ;;
  windows) extld="-Wl,--export-all-symbols -Wl,--exclude-all-symbols $dir/exports_windows.def" ;;
esac

CGO_ENABLED=1 go build -buildmode=c-shared -tags driverlib \
  -ldflags "-extldflags '$extld'" -o "$out" ./pkg/quack
echo "built $out"
