"""
setup.py for the adbc-driver-quack Python wheel.

The Go-built c-shared library must be copied into the package source
tree before the wheel is built. The path to the library is passed
via the ``ADBC_QUACK_LIBRARY`` environment variable, mirroring the
adbc-driver-flightsql convention.

Lives at the repo root (not under python/) so the PyPI README can
just be ``README.md`` — no parent-directory reference or mirror dance.
"""

import os
import shutil
import sys
from pathlib import Path

from setuptools import setup

source_root = Path(__file__).parent
package_dir = source_root / "python" / "adbc_driver_quack"


def _library_suffix() -> str:
    if sys.platform.startswith("linux"):
        return "so"
    if sys.platform == "darwin":
        return "dylib"
    if sys.platform.startswith("win"):
        return "dll"
    return "so"


def _normalize_library_path(p: str) -> str:
    """Translate Git Bash / MSYS unix-style paths to native Windows form.

    On Windows runners (and on dev machines that build via Git Bash),
    `$PWD` is a path like ``/d/a/repo``. ``Path("/d/a/repo")`` on Windows
    rebases at the current drive root, not at the intended ``D:`` drive,
    so the copy below ends up at the wrong location. We translate
    ``/<letter>/<rest>`` to ``<letter>:/<rest>`` to dodge that.
    """
    if not sys.platform.startswith("win") or not p.startswith("/"):
        return p
    rest = p.lstrip("/")
    if len(rest) >= 2 and rest[1] == "/" and rest[0].isalpha():
        return f"{rest[0].upper()}:/{rest[2:]}"
    return p


# Bundle the c-shared library produced by `make -C pkg/quack`.
library = os.environ.get("ADBC_QUACK_LIBRARY")
target = package_dir / f"libadbc_driver_quack.{_library_suffix()}"

if library:
    library = _normalize_library_path(library)
    library_path = Path(library).resolve()
    if library_path != target.resolve() if target.exists() else target:
        shutil.copy(library_path, target)
    else:
        print(f"ADBC_QUACK_LIBRARY already points at {target}; no copy needed.")
elif os.environ.get("_ADBC_IS_SDIST", "").strip().lower() in ("1", "true"):
    print("Building sdist — skipping ADBC_QUACK_LIBRARY check.")
elif target.is_file():
    print(f"Using pre-existing driver library at {target}.")
else:
    raise ValueError(
        "ADBC_QUACK_LIBRARY env var is required when building a wheel "
        "(it should point to the libadbc_driver_quack.{so,dylib,dll} "
        "produced by `make -C pkg/quack`)."
    )


def _read_version(pkg_path: Path) -> str:
    from importlib.util import module_from_spec, spec_from_file_location

    spec = spec_from_file_location("version", pkg_path / "_version.py")
    module = module_from_spec(spec)
    spec.loader.exec_module(module)  # type: ignore[union-attr]
    return module.__version__  # type: ignore[attr-defined]


setup(version=_read_version(package_dir))
