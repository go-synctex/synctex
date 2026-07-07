package synctex

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// minimalSyncTeX is a hand-built sample that mirrors what pdflatex
// emits for a 2-page document with sections + paragraphs. The
// dimensions are sanitised to round numbers so the test reads.
const minimalSyncTeX = `SyncTeX Version:1
Input:1:/work/main.tex
Input:2:/work/intro.tex
Magnification:1000
Unit:1
X Offset:0
Y Offset:0
Content:
{1
[1,5:1000,2000:2000,500,0
h 1,6:1000,2500:2000,500,0
h 1,7:1000,3000:2000,500,0
[2,3:1000,5000:2000,500,0
h 2,4:1000,5500:2000,500,0
}
{2
[1,15:1000,2000:2000,500,0
h 1,16:1000,2500:2000,500,0
}
Postamble:
`

func TestParseAndForward(t *testing.T) {
	f, err := parseStream(strings.NewReader(minimalSyncTeX))
	if err != nil {
		t.Fatalf("parse : %v", err)
	}
	if len(f.Inputs) != 2 {
		t.Fatalf("expected 2 inputs, got %d", len(f.Inputs))
	}
	// Input: paths are filepath.Clean'd, which normalises to the OS separator;
	// compare via ToSlash so the assertion holds on Windows too.
	if filepath.ToSlash(f.Inputs[1]) != "/work/main.tex" || filepath.ToSlash(f.Inputs[2]) != "/work/intro.tex" {
		t.Fatalf("input map wrong : %v", f.Inputs)
	}
	if len(f.Records) != 7 {
		t.Fatalf("expected 7 records (5 from page 1, 2 from page 2), got %d", len(f.Records))
	}
	// Forward : main.tex line 7 → page 1 (the line-7 hbox).
	r, ok := f.Forward("main.tex", 7)
	if !ok {
		t.Fatalf("forward main.tex:7 → no result")
	}
	if r.Page != 1 || r.Line != 7 {
		t.Fatalf("forward main.tex:7 → got page=%d line=%d, want page=1 line=7", r.Page, r.Line)
	}
	// Forward : main.tex line 15 → page 2 (only page where line ≥ 15).
	r, ok = f.Forward("main.tex", 15)
	if !ok || r.Page != 2 {
		t.Fatalf("forward main.tex:15 → got page=%d (want page=2)", r.Page)
	}
	// Forward : intro.tex line 3 → page 1.
	r, ok = f.Forward("intro.tex", 3)
	if !ok || r.Page != 1 {
		t.Fatalf("forward intro.tex:3 → got page=%d (want page=1)", r.Page)
	}
	// Forward : missing file → not ok.
	if _, ok := f.Forward("does-not-exist.tex", 1); ok {
		t.Fatalf("forward missing file should return ok=false")
	}
	// Forward : line past the last record → fallback to last record.
	r, ok = f.Forward("main.tex", 9999)
	if !ok || r.Page != 2 {
		t.Fatalf("forward main.tex:9999 → got page=%d (want fallback to page 2)", r.Page)
	}
}

func TestBackward(t *testing.T) {
	f, err := parseStream(strings.NewReader(minimalSyncTeX))
	if err != nil {
		t.Fatalf("parse : %v", err)
	}
	// Click near the (1000, 2500) record on page 1 → main.tex line 6.
	src, line, ok := f.Backward(1, 1010, 2510)
	if !ok || filepath.ToSlash(src) != "/work/main.tex" || line != 6 {
		t.Fatalf("backward → src=%s line=%d ok=%v (want /work/main.tex line=6)", src, line, ok)
	}
}

func TestParseGzipFile(t *testing.T) {
	// Round-trip through a real .synctex.gz on disk so we exercise
	// the gzip + file open codepath.
	tmp := t.TempDir()
	path := filepath.Join(tmp, "demo.synctex.gz")
	buf := &bytes.Buffer{}
	gz := gzip.NewWriter(buf)
	gz.Write([]byte(minimalSyncTeX))
	gz.Close()
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write gz : %v", err)
	}
	f, err := Parse(path)
	if err != nil {
		t.Fatalf("Parse(gz) : %v", err)
	}
	if len(f.Records) != 7 {
		t.Fatalf("expected 7 records, got %d", len(f.Records))
	}
}
