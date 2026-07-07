package synctex

import (
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseErrors exercises every Parse failure branch.
func TestParseErrors(t *testing.T) {
	// os.Open failure — non-existent path.
	if _, err := Parse(filepath.Join(t.TempDir(), "nope.synctex.gz")); err == nil {
		t.Fatal("Parse(missing) should error")
	}

	// gzip.NewReader failure — a .gz path whose bytes are not gzip.
	tmp := t.TempDir()
	bad := filepath.Join(tmp, "bad.synctex.gz")
	if err := os.WriteFile(bad, []byte("not gzip at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(bad); err == nil {
		t.Fatal("Parse(non-gzip .gz) should error")
	}

	// io.ReadAll failure — a valid gzip header followed by a truncated,
	// corrupt body so decompression aborts mid-stream.
	buf := &bytes.Buffer{}
	gz := gzip.NewWriter(buf)
	gz.Write(bytes.Repeat([]byte("Input:1:/a.tex\n"), 4096))
	gz.Close()
	raw := buf.Bytes()
	truncated := raw[:len(raw)-8] // drop the CRC/size footer + tail
	trunc := filepath.Join(tmp, "trunc.synctex.gz")
	if err := os.WriteFile(trunc, truncated, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(trunc); err == nil {
		t.Fatal("Parse(truncated gzip) should error")
	}

	// Body exceeds maxSyncTeXFileSize — compresses tiny, decompresses huge.
	big := &bytes.Buffer{}
	gz2 := gzip.NewWriter(big)
	gz2.Write(bytes.Repeat([]byte("A"), maxSyncTeXFileSize+16))
	gz2.Close()
	huge := filepath.Join(tmp, "huge.synctex.gz")
	if err := os.WriteFile(huge, big.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(huge); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Parse(oversized) should report exceeds, got %v", err)
	}
}

// TestParsePlainFile covers the non-.gz codepath in Parse (r = f directly).
func TestParsePlainFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "plain.synctex") // no .gz suffix
	if err := os.WriteFile(path, []byte(minimalSyncTeX), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Parse(path)
	if err != nil {
		t.Fatalf("Parse(plain): %v", err)
	}
	if len(f.Records) != 7 {
		t.Fatalf("expected 7 records, got %d", len(f.Records))
	}
}

// TestParseStreamContentBranches drives every switch arm inside the
// Content: loop and every malformed-record branch in parseRecord.
func TestParseStreamContentBranches(t *testing.T) {
	src := strings.Join([]string{
		"SyncTeX Version:1",
		"Input:1:/w/main.tex",
		"Input:99:/w/orphan.tex", // declared but no record references it
		"NotAHeader:ignored",     // pre-Content line that is neither Input nor Content
		"Content:",
		"",                          // empty line inside content
		"h 1,2:3,4:5,6,0",           // record while page==0 → skipped
		"{notanumber",               // '{' with non-numeric page → page stays 0
		"{1",                        // open page 1
		"[1,5:1000,2000:2000,500,0", // box record
		"v 1,6:1,2:3,4,0",           // vbox
		"k 1,7:1,2:3,4,0",           // kern
		"g 1,8:1,2:3,4,0",           // glue
		"x 1,9:1,2:3,4,0",           // rule
		"h 1,10:11,12",              // record with no trailing W,H,D (end<0 path)
		"h",                         // 'h' too short → skip
		"hx 1,2:3,4",                // 'h' not followed by space → skip
		"[bogus",                    // '[' but parseRecord fails (no comma)
		"yunknown 1,2:3,4",          // unknown leading char → default skip
		"[nocomma",                  // parseRecord: comma <= 0
		"[x,5:1,2",                  // parseRecord: tag Atoi err
		"[1,5nocolon",               // parseRecord: colon <= 0
		"[1,z:1,2",                  // parseRecord: line Atoi err
		"[1,5:100",                  // parseRecord: c2 <= 0 (no comma in xy)
		"[1,5:z,2",                  // parseRecord: x Atoi err
		"[1,5:1,z",                  // parseRecord: y Atoi err
		"[99,4:1,2:3,4,0",           // valid record for orphan tag 99 (finish: path lookup ok)
		"Ppage-line-in-content",     // 'P' but not Postamble → continue
		"}",                         // close page → page=0
		"h 1,11:1,2:3,4,0",          // record after close, page==0 → skipped
		"Postamble:",
	}, "\n") + "\n"

	f, err := parseStream(strings.NewReader(src))
	if err != nil {
		t.Fatalf("parseStream: %v", err)
	}
	// Records with a valid line on main.tex: lines 5,6,7,8,9,10 = 6 records,
	// plus the orphan tag-99 record = 7 total appended.
	if len(f.Records) != 7 {
		t.Fatalf("expected 7 records, got %d: %+v", len(f.Records), f.Records)
	}
	// The tag-10 record with no trailing dims must still resolve.
	if r, ok := f.Forward("main.tex", 10); !ok || r.Line != 10 {
		t.Fatalf("forward main.tex:10 → %+v ok=%v", r, ok)
	}
}

// TestParseStreamScannerError forces bufio.Scanner to error via a token
// longer than its max buffer.
func TestParseStreamScannerError(t *testing.T) {
	// A single line far longer than the 4 MiB scanner cap, inside Content.
	giant := "Content:\n[1,5:" + strings.Repeat("9", 5*1024*1024)
	if _, err := parseStream(strings.NewReader(giant)); err == nil {
		t.Fatal("parseStream(giant line) should surface a scanner error")
	}
}

// TestForwardEmptyRecords covers Forward when the source resolves but has
// no indexed records.
func TestForwardEmptyRecords(t *testing.T) {
	src := "SyncTeX Version:1\nInput:2:/w/empty.tex\nContent:\n{1\n}\nPostamble:\n"
	f, err := parseStream(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Forward("empty.tex", 1); ok {
		t.Fatal("Forward on a file with no records should return ok=false")
	}
}

// TestParseStreamNoPostamble covers the natural end-of-scan return (no
// Postamble: line, so the loop exits and finish() runs at the bottom).
func TestParseStreamNoPostamble(t *testing.T) {
	src := "SyncTeX Version:1\nInput:1:/w/main.tex\nContent:\n{1\n[1,5:1,2:3,4,0\n}\n"
	f, err := parseStream(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(f.Records))
	}
}

// TestBackwardNoMatch covers the no-record-on-page branch.
func TestBackwardNoMatch(t *testing.T) {
	f, err := parseStream(strings.NewReader(minimalSyncTeX))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := f.Backward(99, 0, 0); ok {
		t.Fatal("Backward on an empty page should return ok=false")
	}
}

// TestResolveSourceVariants covers the exact / suffix / basename match arms
// and the miss.
func TestResolveSourceVariants(t *testing.T) {
	f := &File{Inputs: map[int]string{1: "/work/sub/main.tex"}}
	if p, ok := f.ResolveSource("/work/sub/main.tex"); !ok || p != "/work/sub/main.tex" {
		t.Fatal("exact match failed")
	}
	if p, ok := f.ResolveSource("sub/main.tex"); !ok || p != "/work/sub/main.tex" {
		t.Fatal("suffix match failed")
	}
	if p, ok := f.ResolveSource("elsewhere/main.tex"); !ok || p != "/work/sub/main.tex" {
		t.Fatal("basename match failed")
	}
	if _, ok := f.ResolveSource("other.tex"); ok {
		t.Fatal("unrelated name should miss")
	}
}

// TestWithProjectRoot exercises sanitiseInputPath through Parse for the
// inside-root, outside-root, on-disk-symlink-resolve, and relative-input
// branches.
func TestWithProjectRoot(t *testing.T) {
	root := t.TempDir()
	// Materialise an in-root file so EvalSymlinks(candidate) succeeds.
	inRoot := filepath.Join(root, "main.tex")
	if err := os.WriteFile(inRoot, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	src := strings.Join([]string{
		"SyncTeX Version:1",
		"Input:1:" + inRoot,        // absolute, inside root → rewritten relative
		"Input:2:/etc/outside.tex", // absolute, outside root → dropped
		"Input:3:sub/rel.tex",      // relative → joined under root, kept
		"Content:",
		"{1",
		"[1,5:1,2:3,4,0",
		"[2,6:1,2:3,4,0",
		"[3,7:1,2:3,4,0",
		"}",
		"Postamble:",
	}, "\n") + "\n"

	path := filepath.Join(root, "doc.synctex")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Parse(path, WithProjectRoot(root))
	if err != nil {
		t.Fatal(err)
	}
	if f.Inputs[1] != "main.tex" {
		t.Fatalf("in-root path should be relative 'main.tex', got %q", f.Inputs[1])
	}
	if _, ok := f.Inputs[2]; ok {
		t.Fatalf("out-of-root path should be dropped, got %q", f.Inputs[2])
	}
	if f.Inputs[3] != filepath.Join("sub", "rel.tex") {
		t.Fatalf("relative path should stay under root, got %q", f.Inputs[3])
	}
}

// TestSanitiseInputPathAbsError forces the filepath.Abs failure branch via
// the test seam.
func TestSanitiseInputPathAbsError(t *testing.T) {
	orig := filepathAbs
	filepathAbs = func(string) (string, error) { return "", errors.New("boom") }
	defer func() { filepathAbs = orig }()

	// projectRoot non-empty so we pass the early return and hit filepathAbs.
	got, ok := sanitiseInputPath("/some/where/file.tex", "proj-root")
	if !ok || got != filepath.Clean("/some/where/file.tex") {
		t.Fatalf("Abs-failure branch should fall back to clean path, got %q ok=%v", got, ok)
	}
}
