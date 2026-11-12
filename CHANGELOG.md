# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/).

## [Unreleased] - 0.2.0

### Added

- `--format github` prints each finding as a GitHub Actions `::error` or
  `::warning` workflow command with its file, line and rule id as the title.
- The GitHub Action annotates findings on their file and line by default,
  alongside the markdown job summary; new input `annotations` (default `true`).

### Changed

- Minimum Go version is now 1.26; CI tests Go 1.26 and 1.27.
- The GitHub Action builds the tool with Go 1.27 by default (`go-version` input).
- The GitHub Action and CI use `actions/setup-go@v7` and `actions/checkout@v7`.
- YAML parsing moved from the archived `gopkg.in/yaml.v3` to `go.yaml.in/yaml/v3` v3.0.6.

## [0.1.0] - 2026-10-21

### Added

- First release: overlay delta report and five rules (`dead-patch`,
  `missing-base`, `cycle`, `shadowed-image`, `dropped-resource`), table, JSON
  and markdown output, and a composite GitHub Action.

[Unreleased]: https://github.com/moveeeax/kustomize-overlay-diff/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/moveeeax/kustomize-overlay-diff/releases/tag/v0.1.0
