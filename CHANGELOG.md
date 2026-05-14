# Changelog

All notable changes to **adbc-driver-quack** are documented here. Format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and this
project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added — Scaffolding

- Repo skeleton: Go module `github.com/gizmodata/adbc-driver-quack`,
  MIT license, README, CHANGELOG, .gitignore, package directory layout
  for `internal/{codec,quacktype,message,transport}`, `driver/quack`,
  `pkg/quack`, `python/`.
- Companion to the
  [`gizmodata/quack-jdbc`](https://github.com/gizmodata/quack-jdbc)
  driver (MIT) — the `internal/` layer will be a clean-room port of the
  matching Java packages in that repo.

## [0.0.0] — _planned_

First substantive release will follow once the codec round-trips and a
minimal read-only ADBC `Driver`/`Database`/`Connection`/`Statement`
returns Arrow `RecordReader`s for a `SELECT 1` against a live Quack
server.
