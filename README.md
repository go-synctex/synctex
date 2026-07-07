<p align="center"><img src="https://raw.githubusercontent.com/go-synctex/brand/main/social/go-synctex-synctex.png" alt="go-synctex/synctex" width="720"></p>

# synctex — go-synctex

[![Docs](https://img.shields.io/badge/docs-mkdocs--material-4F46E5)](https://go-synctex.github.io/docs/)
[![License](https://img.shields.io/badge/license-BSD--3--Clause-blue)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.26.4%2B-00ADD8)](https://go.dev/dl/)
[![Coverage](https://img.shields.io/badge/coverage-100%25-1a7f37)](#tests--coverage)

**A pure-Go (no cgo) parser for TeX's [SyncTeX](https://www.tug.org/TUGboat/tb29-3/tb93laurens.pdf)
output** — the `.synctex.gz` file `pdflatex`/`xelatex`/`lualatex` emit to link a
`.tex` **source line** to the exact **spot on the PDF page**, in both directions.

It answers the two queries an editor/viewer needs:

- **Forward** — `(source file, line)` → `(page, x, y)`: jump the PDF viewer to
  where a source line was typeset.
- **Backward** — `(page, x, y)` → `(source file, line)`: click a glyph in the
  PDF and jump back to the source that produced it.

Zero external dependencies — standard library only. It cross-compiles and embeds
anywhere, is hardened against hostile input (a decompression-size cap guards
against zip-bomb `.synctex.gz` files), and can scope `Input:` paths to a project
root so absolute host paths never leak back to a client.

## Install

```sh
go get github.com/go-synctex/synctex
```

## Usage

```go
package main

import (
	"fmt"

	"github.com/go-synctex/synctex"
)

func main() {
	// Parse the .synctex.gz the TeX engine wrote next to the PDF.
	f, err := synctex.Parse("main.synctex.gz")
	if err != nil {
		panic(err)
	}

	// Forward: source → PDF. "main.tex" line 42 lands here:
	if r, ok := f.Forward("main.tex", 42); ok {
		fmt.Printf("page %d at (%d, %d) sp\n", r.Page, r.X, r.Y)
	}

	// Backward: PDF → source. A click at (x, y) sp on page 2:
	if src, line, ok := f.Backward(2, 32_000_000, 45_000_000); ok {
		fmt.Printf("%s:%d\n", src, line)
	}
}
```

Coordinates are SyncTeX **scaled points** (sp); PDF points are `sp / 65536`. The
raw sp are exposed so callers can apply their own DPI/point scaling (PDF.js, for
instance, wants PDF points).

### Scoping untrusted paths to a project root

`Input:` entries in a `.synctex.gz` are absolute paths under the compile
workdir. When the file is untrusted or its paths would be echoed to a client,
pass `WithProjectRoot` — paths outside the root are dropped and paths inside are
rewritten relative-to-root:

```go
f, err := synctex.Parse("build/main.synctex.gz",
	synctex.WithProjectRoot("/srv/projects/thesis"))
```

## API

```go
// Parse reads a .synctex.gz (or plain .synctex) file from disk.
func Parse(path string, opts ...Option) (*File, error)

// WithProjectRoot scopes Input: paths to root: outside paths are dropped,
// inside paths are rewritten relative-to-root.
func WithProjectRoot(root string) Option

// Forward: (source file, line) → nearest Record (page + x/y in sp).
func (f *File) Forward(sourceFile string, line int) (Record, bool)

// Backward: (page, x, y in sp) → (source file, line) closest to the point.
func (f *File) Backward(page, x, y int) (sourceFile string, line int, ok bool)

// ResolveSource maps an editor-side path to the absolute path SyncTeX recorded.
func (f *File) ResolveSource(rel string) (string, bool)

type Record struct {
	FileTag    int // Input: tag
	Line       int // 1-based source line
	Page       int // 1-based PDF page
	X, Y       int // anchor, in SyncTeX scaled points (sp)
}
```

## Tests & coverage

Pure standard-library logic — the same suite runs identically on every platform,
so there is no interpreter or external oracle to install.

```sh
COVERPKG=$(go list ./... | paste -sd, -)
go test -race -coverpkg="$COVERPKG" -coverprofile=cover.out ./...
go tool cover -func=cover.out | tail -1   # 100.0%
```

**100% test coverage** (including every error and malformed-record branch),
`gofmt` + `go vet` clean, and green across the six 64-bit Go targets — amd64,
arm64, riscv64, loong64, ppc64le, s390x.

## License

BSD-3-Clause — see [LICENSE](LICENSE). Copyright the go-synctex/synctex authors.
