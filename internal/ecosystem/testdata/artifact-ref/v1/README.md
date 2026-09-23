# ArtifactRefV1 conformance corpus (file.cheap, fcheap-local subset)

These fixtures are copied byte-for-byte from file.cheap's own canonical
conformance corpus:

- source: `~/projects/file.cheap/contracts/artifact-ref/v1/{valid,invalid}`
- pinned commit: `23cfdbee2b14ec412b8e66678c74eb63a81dcfec` (file.cheap
  `main`, 2026-09-06)
- `schema.json` at that commit hashes to
  `eb1bbd71132d83b16fdaf2fd475b966cee80d7a12c063aa6993b0ae87c4797a9`
  (recorded here for provenance; the file itself isn't copied because
  monitor's `ArtifactRefV1` validates in Go, not via a JSON Schema
  validator — see `internal/ecosystem/codeintel.go`).

`CHECKSUMS.sha256` pins the exact bytes of every fixture below. Verify with:

```sh
cd internal/ecosystem/testdata/artifact-ref/v1 && shasum -a 256 -c CHECKSUMS.sha256
```

`TestArtifactRefV1CorpusChecksumsMatch` (artifactref_parity_test.go) runs the
same check in `go test`, so an edited fixture that didn't also update
`CHECKSUMS.sha256` fails the build instead of drifting silently.

## Only the fcheap-local provider is represented here

file.cheap's `ArtifactRefV1` covers three provider shapes: `fcheap-local`,
cloud-produced, and external link references. monitor's `ArtifactRefV1`
(`internal/ecosystem/codeintel.go`) implements **only** `fcheap-local` — the
`provider` field is a constant, and there is no cloud or link variant to
construct or accept. file.cheap's cloud/link fixtures
(`valid/cloud-produced.json`, `valid/external-link.json`,
`invalid/cloud-mismatched-uri.json`, `invalid/cloud-signed-web-url.json`,
`invalid/credentialed-link.json`, `invalid/out-of-range-link-port.json`,
`invalid/signed-link.json`) are **intentionally excluded** from this corpus:
monitor's decoder would reject every one of them (wrong `provider`, or shapes
its type can't even represent), but that rejection wouldn't be exercising any
monitor-specific invariant — it would just be re-testing "provider !=
fcheap-local", already covered by `TestArtifactRefV1RejectsFileCheapsInvalidShapes`'s
`wrong $schema`/`missing $schema` cases.

## `valid/` — must pass `decodeArtifactRef` + `Validate()`

- `local-generic-minimal.json` — the minimal fcheap-local shape, no producer.
- `local-vidtrace-produced.json` — fcheap-local with a full `producer` block.

## `invalid/` — must be rejected, at decode or at `Validate()`

- `mismatched-uri.json` — `uri` doesn't match `fcheap://stash/<artifact_id>`.
- `local-web-url.json` — `web_url` is link/cloud-only; forbidden for
  fcheap-local.
- `unsafe-entrypoint.json` — `producer.entrypoint` escapes with `../`.
- `executable-native-schema.json` — `producer.native_schema` is a
  `javascript:` URI, not `urn:`/`https:`.
- `empty-urn-native-schema.json` — `producer.native_schema` is a bare `"urn:"`
  with no opaque part.
- `legacy-integrity.json` — carries an unknown top-level `integrity` field,
  rejected by `decodeArtifactRef`'s `DisallowUnknownFields`.
