// Package synctex parses TeX's synctex.gz output and answers the
// two queries the editor needs :
//
//	Forward  : (file, line)        → (page, x, y, height)
//	Backward : (page, x, y)        → (file, line)            [V0.2]
//
// The file format is well-documented in the synctex source (Jérôme
// Laurens, "SyncTeX: introduction to the engine"), but the gist is :
//
//	SyncTeX Version:1
//	Input:N:/path/to/file.tex          ← fileTag → path map
//	...
//	Magnification:1000
//	Unit:1
//	X Offset:0
//	Y Offset:0
//	Content:
//	{P                                  ← page block (P is page number)
//	  [N,L:X,Y:W,H,D                    ← node : tag N, line L, x/y/...
//	  h N,L:X,Y:W,H,D
//	  ...
//	}
//	...
//	Postamble:
//
// We only care about the records that map line → page+pos, so the
// parser is line-oriented + ignores boxes we don't understand.
package synctex

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// filepathAbs is a seam over filepath.Abs so the (practically
// unreachable) working-directory-lookup failure branch in
// sanitiseInputPath can be exercised by the tests.
var filepathAbs = filepath.Abs

// maxSyncTeXFileSize caps the decompressed-into-memory size before
// scanning so a hostile / runaway .synctex.gz can't OOM the server.
// Real-world synctex files are sub-MB ; 32 MB is plenty of headroom.
const maxSyncTeXFileSize = 32 * 1024 * 1024

// Option tunes Parse behaviour. The zero value is the legacy default
// (no project-root sanitisation) ; handlers that know the project
// boundary pass WithProjectRoot so untrusted Input: paths can't be
// echoed back to the SPA verbatim.
type Option func(*parseOptions)

type parseOptions struct {
	projectRoot string
}

// WithProjectRoot scopes Input: paths to the given project root.
// Paths that fall outside the root are dropped with no record kept,
// and paths inside the root are rewritten to relative-to-root form
// so Backward() never leaks absolute host paths.
func WithProjectRoot(root string) Option {
	return func(o *parseOptions) { o.projectRoot = root }
}

// Record is one node from the synctex stream — a hit point that
// pins a source line to a PDF coordinate.
type Record struct {
	FileTag int
	Line    int
	Page    int
	// X / Y are in synctex "scaled points" (sp). The unit conversion
	// to PDF points is X/65536, but we expose the raw sp so callers
	// can decide what to do (PDF.js wants PDF points + DPI scaling).
	X int
	Y int
}

type File struct {
	Inputs  map[int]string // tag → absolute source path
	Records []Record
	// LineIndex maps (sourceAbsPath → sorted list of records on that
	// file) so Forward() can binary-search the closest line.
	lineIndex map[string][]Record
}

// Parse reads a .synctex.gz file from disk. Returns a File with the
// indexed records ready for Forward queries.
func Parse(path string, opts ...Option) (*File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		gz, gerr := gzip.NewReader(f)
		if gerr != nil {
			return nil, fmt.Errorf("synctex gzip : %w", gerr)
		}
		defer gz.Close()
		r = gz
	}
	// Cap the decompressed body at maxSyncTeXFileSize before scanning ;
	// LimitReader + a size check guards against zip-bomb-style inputs.
	limited := io.LimitReader(r, maxSyncTeXFileSize+1)
	body, rerr := io.ReadAll(limited)
	if rerr != nil {
		return nil, fmt.Errorf("synctex read : %w", rerr)
	}
	if len(body) > maxSyncTeXFileSize {
		return nil, fmt.Errorf("synctex : body exceeds %d bytes", maxSyncTeXFileSize)
	}
	return parseStream(bytes.NewReader(body), opts...)
}

func parseStream(r io.Reader, opts ...Option) (*File, error) {
	po := parseOptions{}
	for _, o := range opts {
		o(&po)
	}
	out := &File{Inputs: make(map[int]string)}
	br := bufio.NewScanner(r)
	br.Buffer(make([]byte, 64*1024), 4*1024*1024)
	page := 0
	inContent := false
	for br.Scan() {
		line := br.Text()
		if !inContent {
			if strings.HasPrefix(line, "Input:") {
				// Input:N:/path
				rest := line[len("Input:"):]
				if idx := strings.Index(rest, ":"); idx > 0 {
					tag, _ := strconv.Atoi(rest[:idx])
					raw := rest[idx+1:]
					if sanitised, ok := sanitiseInputPath(raw, po.projectRoot); ok {
						out.Inputs[tag] = sanitised
					}
				}
				continue
			}
			if strings.HasPrefix(line, "Content:") {
				inContent = true
				continue
			}
			continue
		}
		// Inside Content:
		if len(line) == 0 {
			continue
		}
		switch line[0] {
		case '{':
			// `{P` opens page block P.
			if p, err := strconv.Atoi(strings.TrimSpace(line[1:])); err == nil {
				page = p
			}
			continue
		case '}':
			page = 0
			continue
		case 'P':
			if strings.HasPrefix(line, "Postamble:") {
				return finish(out), nil
			}
			continue
		}
		// Records that pin a source line look like :
		//   [N,L:X,Y:...     box-open
		//   h N,L:X,Y:...    hbox
		//   v N,L:X,Y:...    vbox
		//   k N,L:X,Y:...    kern
		//   g N,L:X,Y:...    glue
		//   x N,L:X,Y:...    rule
		// We accept the leading character + parse the N,L:X,Y prefix.
		// We DON'T parse the trailing W,H,D dimensions since the
		// forward query only needs the anchor coordinate.
		if page == 0 {
			continue
		}
		head := line[0]
		body := ""
		switch head {
		case '[':
			body = line[1:]
		case 'h', 'v', 'k', 'g', 'x':
			if len(line) > 2 && line[1] == ' ' {
				body = line[2:]
			} else {
				continue
			}
		default:
			continue
		}
		rec, ok := parseRecord(body, page)
		if ok {
			out.Records = append(out.Records, rec)
		}
	}
	if err := br.Err(); err != nil {
		return nil, err
	}
	return finish(out), nil
}

func finish(out *File) *File {
	out.lineIndex = make(map[string][]Record)
	for _, r := range out.Records {
		path := out.Inputs[r.FileTag]
		if path == "" {
			continue
		}
		out.lineIndex[path] = append(out.lineIndex[path], r)
	}
	for k := range out.lineIndex {
		recs := out.lineIndex[k]
		sort.Slice(recs, func(i, j int) bool { return recs[i].Line < recs[j].Line })
		out.lineIndex[k] = recs
	}
	return out
}

// sanitiseInputPath cleans a SyncTeX Input: path. When projectRoot is
// empty (legacy callers) the path is just filepath.Clean'd. Otherwise
// absolute paths outside the root are dropped (ok=false) and paths
// inside the root are rewritten to relative-to-root so handlers don't
// echo absolute host paths back to the SPA.
func sanitiseInputPath(raw, projectRoot string) (string, bool) {
	clean := filepath.Clean(raw)
	if projectRoot == "" {
		return clean, true
	}
	rootAbs, err := filepathAbs(projectRoot)
	if err != nil {
		return clean, true
	}
	// Resolve symlinks on the root before comparison. If the
	// candidate exists on disk, resolve symlinks on it too so a path
	// like `.workspace/leak → /etc` lands as /etc and gets rejected.
	// EvalSymlinks fails on non-existent paths — that's fine, fall
	// back to the lexical clean (covers the synthetic .synctex paths
	// the engine emits but never materialises on disk).
	if resolved, rerr := filepath.EvalSymlinks(rootAbs); rerr == nil {
		rootAbs = resolved
	}
	candidate := clean
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(rootAbs, candidate)
	}
	if resolved, rerr := filepath.EvalSymlinks(candidate); rerr == nil {
		candidate = resolved
	}
	rel, err := filepath.Rel(rootAbs, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

// parseRecord parses "N,L:X,Y:..." or "N,L:X,Y" (trailing portion
// optional). Returns false on a malformed line so the loop can skip
// it without aborting the whole parse.
func parseRecord(s string, page int) (Record, bool) {
	// N,L : tag + line
	comma := strings.IndexByte(s, ',')
	if comma <= 0 {
		return Record{}, false
	}
	tag, err := strconv.Atoi(s[:comma])
	if err != nil {
		return Record{}, false
	}
	rest := s[comma+1:]
	colon := strings.IndexByte(rest, ':')
	if colon <= 0 {
		return Record{}, false
	}
	line, err := strconv.Atoi(rest[:colon])
	if err != nil {
		return Record{}, false
	}
	xy := rest[colon+1:]
	// X,Y : co-ordinates
	c2 := strings.IndexByte(xy, ',')
	if c2 <= 0 {
		return Record{}, false
	}
	x, err := strconv.Atoi(xy[:c2])
	if err != nil {
		return Record{}, false
	}
	rest2 := xy[c2+1:]
	end := strings.IndexAny(rest2, ":,")
	if end < 0 {
		end = len(rest2)
	}
	y, err := strconv.Atoi(rest2[:end])
	if err != nil {
		return Record{}, false
	}
	return Record{FileTag: tag, Line: line, Page: page, X: x, Y: y}, true
}

// ResolveSource maps an editor-side source path to the absolute
// path SyncTeX would have recorded. The editor passes a project-
// relative file ("main.tex") ; the synctex Input: entries are
// absolute paths under the compile workdir. We match by suffix.
func (f *File) ResolveSource(rel string) (string, bool) {
	for _, p := range f.Inputs {
		if p == rel || strings.HasSuffix(p, "/"+rel) || filepath.Base(p) == filepath.Base(rel) {
			return p, true
		}
	}
	return "", false
}

// Forward : given a (source file, line), pick the best record to
// jump the PDF viewer to. We pick the record on the nearest line ≥
// the query, falling back to the largest line < query when nothing
// at or after exists.
func (f *File) Forward(sourceFile string, line int) (Record, bool) {
	abs, ok := f.ResolveSource(sourceFile)
	if !ok {
		return Record{}, false
	}
	recs := f.lineIndex[abs]
	if len(recs) == 0 {
		return Record{}, false
	}
	// Binary search for the first record with line >= query.
	i := sort.Search(len(recs), func(i int) bool { return recs[i].Line >= line })
	if i < len(recs) {
		return recs[i], true
	}
	// All records are before `line` ; return the last.
	return recs[len(recs)-1], true
}

// Backward : given a (page, x, y) in synctex sp, pick the source
// (file, line) closest to that point. V0.2 — used once the PDF.js
// viewer is wired so the user can click on a glyph + jump back.
func (f *File) Backward(page int, x, y int) (sourceFile string, line int, ok bool) {
	bestDist := int64(-1)
	bestRec := Record{}
	for _, r := range f.Records {
		if r.Page != page {
			continue
		}
		dx := int64(r.X - x)
		dy := int64(r.Y - y)
		d := dx*dx + dy*dy
		if bestDist < 0 || d < bestDist {
			bestDist = d
			bestRec = r
		}
	}
	if bestDist < 0 {
		return "", 0, false
	}
	return f.Inputs[bestRec.FileTag], bestRec.Line, true
}
