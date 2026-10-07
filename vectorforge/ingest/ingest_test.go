package ingest

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// checkCoverage asserts the invariants every chunker must satisfy.
func checkCoverage(t *testing.T, label string, content string, pieces []Piece, opts Options) {
	t.Helper()
	if kind, ok := KindForPath(label); ok {
		opts = opts.For(kind)
	}
	lines := splitLines(content)
	if len(pieces) == 0 {
		t.Fatalf("%s: no chunks", label)
	}
	for i, p := range pieces {
		if len(p.Text) > opts.MaxBytes {
			t.Fatalf("%s chunk %d: %d bytes exceeds max %d", label, i, len(p.Text), opts.MaxBytes)
		}
		if p.StartLine < 1 || p.EndLine < p.StartLine || p.EndLine > len(lines) {
			t.Fatalf("%s chunk %d: bad line range %d-%d of %d", label, i, p.StartLine, p.EndLine, len(lines))
		}
		// Every chunk's text must be the actual source for its line range.
		want := strings.TrimRight(strings.Join(lines[p.StartLine-1:p.EndLine], "\n"), " \t\n")
		if len(p.Text) <= opts.MaxBytes && !strings.HasPrefix(want, p.Text) {
			t.Fatalf("%s chunk %d (%q): text does not match lines %d-%d", label, i, p.Section, p.StartLine, p.EndLine)
		}
	}
	// No non-blank source line may be lost.
	covered := make([]bool, len(lines))
	for _, p := range pieces {
		for l := p.StartLine; l <= p.EndLine; l++ {
			covered[l-1] = true
		}
	}
	for i, ok := range covered {
		if !ok && strings.TrimSpace(lines[i]) != "" {
			t.Fatalf("%s: line %d (%q) is in no chunk", label, i+1, lines[i])
		}
	}
}

func TestGoChunkerFollowsDeclarations(t *testing.T) {
	path := filepath.Join(repoRoot(t), "btree.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content, err := Sanitize(raw)
	if err != nil {
		t.Fatal(err)
	}
	pieces := Chunk(KindGo, "btree.go", content, DefaultOptions)
	checkCoverage(t, "btree.go", content, pieces, DefaultOptions)
	var insert *Piece
	for i := range pieces {
		if strings.Contains(pieces[i].Section, "func (tree *btree) insert") {
			insert = &pieces[i]
			break
		}
	}
	if insert == nil {
		t.Fatalf("no chunk is labelled with the insert method; sections: %v", sections(pieces))
	}
	if !strings.Contains(insert.Text, "func (tree *btree) insert(key, val []byte)") {
		t.Fatalf("insert chunk does not start at the declaration:\n%s", insert.Text)
	}
	if !strings.HasPrefix(pieces[0].Section, "package byodb") {
		t.Fatalf("first chunk section %q", pieces[0].Section)
	}
}

func sections(pieces []Piece) []string {
	out := make([]string, len(pieces))
	for i, p := range pieces {
		out[i] = p.Section
	}
	return out
}

func TestGoChunkerSplitsLargeFunctions(t *testing.T) {
	var b strings.Builder
	b.WriteString("package big\n\n// Huge has a very long body.\nfunc Huge() {\n")
	for i := 0; i < 200; i++ {
		b.WriteString("\tx := computeSomethingQuiteLong(1, 2, 3)\n")
	}
	b.WriteString("}\n\nfunc Small() {}\n")
	content := b.String()
	pieces := Chunk(KindGo, "big.go", content, DefaultOptions)
	checkCoverage(t, "big.go", content, pieces, DefaultOptions)
	parts := 0
	for _, p := range pieces {
		if strings.HasPrefix(p.Section, "func Huge (part ") {
			parts++
		}
	}
	if parts < 3 {
		t.Fatalf("expected Huge to be split into parts, sections %v", sections(pieces))
	}
}

func TestGoChunkerFallsBackOnSyntaxErrors(t *testing.T) {
	content := "package broken\n\nfunc (\n\nthis is not go\n"
	pieces := Chunk(KindGo, "broken.go", content, DefaultOptions)
	checkCoverage(t, "broken.go", content, pieces, DefaultOptions)
}

func TestMarkdownChunkerUsesHeadings(t *testing.T) {
	path := filepath.Join(repoRoot(t), "docs", "DURABILITY.md")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content, _ := Sanitize(raw)
	pieces := Chunk(KindMarkdown, "docs/DURABILITY.md", content, DefaultOptions)
	checkCoverage(t, "DURABILITY.md", content, pieces, DefaultOptions)
	found := false
	for _, p := range pieces {
		if p.Section == "Durability and process-ownership contract > Commit protocol" && strings.Contains(p.Text, "durable commit point") {
			found = true
		}
	}
	if !found {
		t.Fatalf("commit-protocol section not found: %v", sections(pieces))
	}
}

func TestMarkdownKeepsFencesAndIgnoresHashesInCode(t *testing.T) {
	content := "# Title\n\nIntro paragraph that is long enough to stand on its own as a section of documentation text here.\n" +
		strings.Repeat("More words to make the section large enough. ", 5) + "\n\n" +
		"## Setup\n\n```sh\n# not a heading\nmake test\n\nmake verify\n```\n\nAfter the fence.\n"
	pieces := Chunk(KindMarkdown, "x.md", content, DefaultOptions)
	checkCoverage(t, "x.md", content, pieces, DefaultOptions)
	for _, p := range pieces {
		if strings.Contains(p.Section, "not a heading") {
			t.Fatal("a comment inside a code fence became a heading")
		}
		if strings.Contains(p.Text, "make test") && !strings.Contains(p.Text, "make verify") {
			t.Fatal("fenced block was split")
		}
	}
}

func TestProseOverlap(t *testing.T) {
	var paragraphs []string
	for i := 0; i < 12; i++ {
		if i%2 == 0 {
			paragraphs = append(paragraphs, "Short bridging sentence number "+itoa(i)+".")
		} else {
			paragraphs = append(paragraphs, strings.Repeat("Long paragraph text "+itoa(i)+" ", 20))
		}
	}
	content := strings.Join(paragraphs, "\n\n")
	pieces := Chunk(KindText, "notes.txt", content, DefaultOptions)
	checkCoverage(t, "notes.txt", content, pieces, DefaultOptions)
	overlaps := 0
	for i := 1; i < len(pieces); i++ {
		if pieces[i].StartLine <= pieces[i-1].EndLine {
			overlaps++
		}
	}
	if overlaps == 0 {
		t.Fatalf("expected overlapping prose chunks: %+v", pieces)
	}
}

func TestPythonChunker(t *testing.T) {
	content := `"""Module docstring.

def not_a_function_inside_docstring():
"""
import os

CONSTANT = 3


@decorator
def first(a, b):
    return a + b


class Model:
    """A class."""

    def method(self):
        return 1


async def fetch():
    pass
`
	pieces := Chunk(KindPython, "m.py", content, DefaultOptions)
	checkCoverage(t, "m.py", content, pieces, DefaultOptions)
	all := strings.Join(sections(pieces), "|")
	for _, want := range []string{"module", "def first", "class Model", "def fetch"} {
		if !strings.Contains(all, want) {
			t.Fatalf("missing %q in %s", want, all)
		}
	}
	if strings.Contains(all, "not_a_function") {
		t.Fatal("treated docstring text as a definition")
	}
	for _, p := range pieces {
		if strings.Contains(p.Text, "def first") && !strings.Contains(p.Text, "@decorator") {
			t.Fatal("decorator separated from its function")
		}
	}
}

func TestPythonSplitsLargeClassesByMethod(t *testing.T) {
	var b strings.Builder
	b.WriteString("class Big:\n")
	for i := 0; i < 12; i++ {
		b.WriteString("    def m" + itoa(i) + "(self):\n")
		for j := 0; j < 6; j++ {
			b.WriteString("        value = self.compute_something_long(" + itoa(j) + ")\n")
		}
		b.WriteString("\n")
	}
	content := b.String()
	pieces := Chunk(KindPython, "big.py", content, DefaultOptions)
	checkCoverage(t, "big.py", content, pieces, DefaultOptions)
	if !strings.Contains(strings.Join(sections(pieces), "|"), "class Big.m7") {
		t.Fatalf("methods not labelled: %v", sections(pieces))
	}
}

func TestScriptChunker(t *testing.T) {
	content := "import x from 'y';\n\nexport function a() {\n  return 1;\n}\n\nexport const b = () => 2;\n\ninterface C { d: string }\n"
	pieces := Chunk(KindTypeScript, "a.ts", content, DefaultOptions)
	checkCoverage(t, "a.ts", content, pieces, DefaultOptions)
}

func TestSanitize(t *testing.T) {
	got, err := Sanitize([]byte("\ufeffline1\r\nline2\x01\x07\tend\r"))
	if err != nil || got != "line1\nline2\tend\n" {
		t.Fatalf("sanitize: %q %v", got, err)
	}
	if _, err := Sanitize([]byte("abc\x00def")); !errors.Is(err, ErrBinary) {
		t.Fatalf("NUL: %v", err)
	}
	if _, err := Sanitize([]byte{0xff, 0xfe, 'a'}); !errors.Is(err, ErrEncoded) {
		t.Fatalf("invalid UTF-8: %v", err)
	}
}

func TestLongLinesAreCutAtRuneBoundaries(t *testing.T) {
	content := strings.Repeat("é", 3000) // 6,000 bytes on one line
	pieces := Chunk(KindText, "wide.txt", content, DefaultOptions)
	total := 0
	for _, p := range pieces {
		if len(p.Text) > DefaultOptions.MaxBytes || !strings.HasPrefix(content[total:], p.Text) {
			t.Fatalf("bad cut at byte %d", total)
		}
		total += len(p.Text)
	}
	if total != len(content) {
		t.Fatalf("lost bytes: %d of %d", total, len(content))
	}
}

func TestStableIDs(t *testing.T) {
	a, b := DocumentID("byodb", "btree.go"), DocumentID("byodb", "btree.go")
	if a != b || len(a) != 16 || a == DocumentID("other", "btree.go") || a == DocumentID("byodb", "kv.go") {
		t.Fatal("document ids are not stable and distinct")
	}
	if ContentHash([]byte("x")) == ContentHash([]byte("y")) {
		t.Fatal("content hash collision")
	}
}

func TestDiscover(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("README.md", "# hi")
	write("pkg/a.go", "package pkg")
	write("pkg/a_test.go", "package pkg")
	write("node_modules/x/index.js", "x")
	write(".git/config", "x")
	write("pkg/image.png", "x")
	write("tools/run.py", "print(1)")
	write("big.txt", strings.Repeat("x", MaxFileBytes+1))
	files, skips, err := Discover(Source{Name: "t", Root: root, Exclude: []string{"*_test.go"}})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range files {
		got = append(got, f.RelPath)
	}
	if strings.Join(got, ",") != "README.md,pkg/a.go,tools/run.py" {
		t.Fatalf("discovered %v", got)
	}
	if len(skips) != 1 || skips[0].RelPath != "big.txt" {
		t.Fatalf("skips %v", skips)
	}
	files, _, err = Discover(Source{Name: "t", Root: root, Include: []string{"tools"}})
	if err != nil || len(files) != 1 || files[0].RelPath != "tools/run.py" {
		t.Fatalf("include: %v %v", files, err)
	}
	if _, _, err := Discover(Source{Name: "t", Root: root, Include: []string{"../etc"}}); err == nil {
		t.Fatal("accepted an include outside the root")
	}
	if _, _, err := Discover(Source{Name: "bad name", Root: root}); err == nil {
		t.Fatal("accepted a source name with a space")
	}
}

func TestCleanUploadPath(t *testing.T) {
	for _, ok := range []string{"notes/a.md", "a.txt"} {
		if _, err := CleanUploadPath(ok); err != nil {
			t.Fatalf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "../a.md", "a/../../b", "/abs/x.md", "a//b.md"} {
		if _, err := CleanUploadPath(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}
