# Third-party notices

Rin is licensed under the [MIT License](LICENSE). The table below is the union
of external Go modules linked by CGO-disabled builds of `cmd/rin`,
`cmd/rin-control`, and `cmd/rin-mcp` for `darwin/arm64`, `linux/amd64`, and
`windows/amd64`. It was derived from `go list -deps`; test-only and tool-only
modules are excluded.

License labels are summaries of the license files in the downloaded module
archives, not replacements for those terms. `all` means the module is in the
dependency closure of all three commands on at least one evaluated target;
otherwise the table names the only command that links the module. Target-
specific dependency pruning can still omit a listed module from one artifact.

| Module | Version | Linked by | License files verified in module archive |
| --- | --- | --- | --- |
| [`github.com/dustin/go-humanize`](https://pkg.go.dev/github.com/dustin/go-humanize@v1.0.1) | `v1.0.1` | all | MIT (`LICENSE`) |
| [`github.com/google/jsonschema-go`](https://pkg.go.dev/github.com/google/jsonschema-go@v0.4.3) | `v0.4.3` | `rin-mcp` | MIT (`LICENSE`) |
| [`github.com/google/uuid`](https://pkg.go.dev/github.com/google/uuid@v1.6.0) | `v1.6.0` | all | BSD-3-Clause (`LICENSE`) |
| [`github.com/mattn/go-isatty`](https://pkg.go.dev/github.com/mattn/go-isatty@v0.0.24) | `v0.0.24` | all | MIT (`LICENSE`) |
| [`github.com/modelcontextprotocol/go-sdk`](https://pkg.go.dev/github.com/modelcontextprotocol/go-sdk@v1.7.0-pre.3) | `v1.7.0-pre.3` | `rin-mcp` | Apache-2.0 and MIT (`LICENSE`) |
| [`github.com/ncruces/go-strftime`](https://pkg.go.dev/github.com/ncruces/go-strftime@v1.0.0) | `v1.0.0` | all | MIT (`LICENSE`) |
| [`github.com/remyoudompheng/bigfft`](https://pkg.go.dev/github.com/remyoudompheng/bigfft@v0.0.0-20230129092748-24d4a6f8daec) | `v0.0.0-20230129092748-24d4a6f8daec` | all | BSD-3-Clause (`LICENSE`) |
| [`github.com/santhosh-tekuri/jsonschema/v6`](https://pkg.go.dev/github.com/santhosh-tekuri/jsonschema/v6@v6.0.2) | `v6.0.2` | all | Apache-2.0 (`LICENSE`) |
| [`github.com/segmentio/asm`](https://pkg.go.dev/github.com/segmentio/asm@v1.1.3) | `v1.1.3` | `rin-mcp` | MIT (`LICENSE`) |
| [`github.com/segmentio/encoding`](https://pkg.go.dev/github.com/segmentio/encoding@v0.5.4) | `v0.5.4` | `rin-mcp` | MIT (`LICENSE`) |
| [`github.com/yosida95/uritemplate/v3`](https://pkg.go.dev/github.com/yosida95/uritemplate/v3@v3.0.2) | `v3.0.2` | `rin-mcp` | BSD-3-Clause (`LICENSE`) |
| [`go.yaml.in/yaml/v3`](https://pkg.go.dev/go.yaml.in/yaml/v3@v3.0.5) | `v3.0.5` | all | MIT and Apache-2.0 (`LICENSE`, `NOTICE`) |
| [`golang.org/x/oauth2`](https://pkg.go.dev/golang.org/x/oauth2@v0.35.0) | `v0.35.0` | `rin-mcp` | BSD-3-Clause (`LICENSE`) |
| [`golang.org/x/sync`](https://pkg.go.dev/golang.org/x/sync@v0.21.0) | `v0.21.0` | `rin-mcp` | BSD-3-Clause (`LICENSE`) |
| [`golang.org/x/sys`](https://pkg.go.dev/golang.org/x/sys@v0.47.0) | `v0.47.0` | all | BSD-3-Clause (`LICENSE`) |
| [`golang.org/x/text`](https://pkg.go.dev/golang.org/x/text@v0.39.0) | `v0.39.0` | all | BSD-3-Clause (`LICENSE`) |
| [`golang.org/x/time`](https://pkg.go.dev/golang.org/x/time@v0.15.0) | `v0.15.0` | `rin-mcp` | BSD-3-Clause (`LICENSE`) |
| [`modernc.org/libc`](https://pkg.go.dev/modernc.org/libc@v1.74.4) | `v1.74.4` | all | BSD-3-Clause plus component terms (`LICENSE`, `LICENSE-3RD-PARTY.md`) |
| [`modernc.org/mathutil`](https://pkg.go.dev/modernc.org/mathutil@v1.7.1) | `v1.7.1` | all | BSD-3-Clause (`LICENSE`, `mersenne/LICENSE`) |
| [`modernc.org/memory`](https://pkg.go.dev/modernc.org/memory@v1.11.0) | `v1.11.0` | all | BSD-3-Clause plus component notices (`LICENSE`, `LICENSE-MMAP-GO`, `LICENSE-GO`, `LICENSE-LOGO`) |
| [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite@v1.56.0) | `v1.56.0` | all | BSD-3-Clause (`LICENSE`) |

The versions in `go.mod` and integrity hashes in `go.sum` are authoritative.
Build tags, target operating systems, or dependency changes can alter a
binary's dependency closure. Release packaging should therefore regenerate
this inventory for every distributed target and include the complete
applicable license, notice, copyright, and attribution texts from the exact
module archives used to build it.
