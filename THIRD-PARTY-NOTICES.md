# Third-party notices

Rin is licensed under the [MIT License](LICENSE). The Go module also uses the
following third-party packages under their own terms:

| Module | Version | License | Source and license text |
| --- | --- | --- | --- |
| `github.com/santhosh-tekuri/jsonschema/v6` | `v6.0.2` | Apache-2.0 | [source](https://github.com/santhosh-tekuri/jsonschema/tree/v6.0.2), [license](https://github.com/santhosh-tekuri/jsonschema/blob/v6.0.2/LICENSE) |
| `golang.org/x/text` | `v0.14.0` | BSD-3-Clause | [source](https://cs.opensource.google/go/x/text/+/v0.14.0), [license](https://cs.opensource.google/go/x/text/+/v0.14.0:LICENSE) |

The versions in `go.mod` and integrity hashes in `go.sum` are authoritative.
Go module downloads include each dependency's complete license file. Binary
distributors must reproduce the applicable license and attribution text in
their distribution materials.

Repository CI additionally uses OpenSpiel `2.0.1` under Apache-2.0 as a
decision-semantics test oracle. It is installed with pinned wheel hashes,
`--no-deps`, and is not linked into or redistributed with Rin binaries.
[Source](https://github.com/google-deepmind/open_spiel/tree/v2.0.1) and
[license](https://github.com/google-deepmind/open_spiel/blob/v2.0.1/LICENSE).
