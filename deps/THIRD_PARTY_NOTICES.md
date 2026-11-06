# Cutly third-party notices

This directory belongs to the independently buildable `github.com/CutlyLab/Cutly` module. It is
the notice source for that module; the Manyfy host repository's notices do not replace it.

`dependency-lock.json` is the normative machine-readable classifier. It separates the current
Cutly test/module graph from the prospective text-shaping production/module graph. Candidate
entries are reviewed and pinned only: they are absent from `cutly/go.mod`, not linked into a Cutly
binary, and do not enable a Text capability. Notices are checked in now so a later provider cannot
silently change dependency or license identity.

## Current Cutly module graph

### github.com/google/jsonschema-go v0.4.2

- Classification: `current_test` / `test_import` / `linked_package_count=1`
- Role: imported only by Cutly conformance tests; no current production package imports it
- Source: <https://github.com/google/jsonschema-go/tree/v0.4.2>
- License: MIT
- Text: `licenses/github.com_google_jsonschema-go_v0.4.2.LICENSE`

### github.com/google/go-cmp v0.7.0

- Classification: `current_module_graph_only` / `upstream_test` / `linked_package_count=0`
- Role: required by jsonschema-go's `go.mod` for upstream tests; no package is imported by Cutly
- Source: <https://github.com/google/go-cmp/tree/v0.7.0>
- License: BSD-3-Clause
- Text: `licenses/github.com_google_go-cmp_v0.7.0.LICENSE`

## Candidate text-shaping graph (not linked)

The prospective production package graph is frozen for roots
`github.com/go-text/typesetting/font` and `github.com/go-text/typesetting/harfbuzz`. With
`CGO_ENABLED=0`, its eleven external packages were identical on `linux/amd64`, `windows/amd64`,
and `js/wasm`. The production provider must re-resolve and refreeze the actual graph for its exact
toolchain, targets, build tags, and imports before it advertises capability.

### github.com/go-text/typesetting v0.3.5

- Classification: `candidate_production` / `production_import` / `linked_package_count=8`
- Role: proposed pure-Go font and HarfBuzz shaping implementation
- Source: <https://github.com/go-text/typesetting/tree/v0.3.5>
- Selected license: BSD-3-Clause from `Unlicense OR BSD-3-Clause`
- Text: `licenses/github.com_go-text_typesetting_v0.3.5.LICENSE`

The module's `harfbuzz` package is a port based on HarfBuzz 12.3.0 commit
`5a31dd02f0b32de12336f72db9297bfe94cf0da1` and carries a separate byte-identical upstream MIT
notice: `licenses/github.com_go-text_typesetting_v0.3.5.harfbuzz.LICENSE`.

### golang.org/x/image v0.23.0

- Classification: `candidate_production_transitive` / `production_import` / `linked_package_count=3`
- Role: proposed transitive production packages reached by go-text font parsing
- Source: <https://go.googlesource.com/image/+/refs/tags/v0.23.0>
- License: BSD-3-Clause
- Text: `licenses/golang.org_x_image_v0.23.0.LICENSE`
- Patent grant: `licenses/golang.org_x_image_v0.23.0.PATENTS`

### github.com/go-text/typesetting-utils 4ffe8874dabc

- Classification: `candidate_test_generate` / `module_graph_only` / `linked_package_count=0`
- Role: upstream test/generation module named by go-text/typesetting v0.3.5; no selected package
- Source: <https://github.com/go-text/typesetting-utils/commit/4ffe8874dabcb8a86a3c8534d2545ea5b95304ea>
- Selected license: BSD-3-Clause from `Unlicense OR BSD-3-Clause`
- Byte-identical upstream text: `licenses/github.com_go-text_typesetting-utils_4ffe8874dabc.LICENSE`

### golang.org/x/text v0.21.0

- Classification: `candidate_module_graph_transitive` / `module_graph_only` / `linked_package_count=0`
- Role: selected only by prospective MVS through x/image's `go.mod`; no selected package imports it
- Source: <https://go.googlesource.com/text/+/refs/tags/v0.21.0>
- License: BSD-3-Clause
- Text: `licenses/golang.org_x_text_v0.21.0.LICENSE`
- Patent grant: `licenses/golang.org_x_text_v0.21.0.PATENTS`

The candidate lock does not imply that Cutly will execute go-text's general WOFF, CFF, variable,
color, SVG, AAT, fallback, or system-font paths. A future provider remains responsible for a strict
Cutly-owned SFNT/static-glyf validation boundary before the same immutable bytes reach shaping.
