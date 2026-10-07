// Package ingest discovers source files, sanitizes their text and splits them
// into chunks along structural boundaries: Markdown sections, Go and Python
// top-level declarations, and paragraphs for plain text.
package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path"
	"strings"
	"unicode/utf8"
)

// ChunkerVersion is stored with every document. Changing chunking behaviour
// must change this string so that incremental ingestion re-chunks files whose
// content hash is otherwise unchanged.
const ChunkerVersion = "vf-chunker-2"

// Kind identifies how a file is chunked.
type Kind string

const (
	KindMarkdown   Kind = "markdown"
	KindGo         Kind = "go"
	KindPython     Kind = "python"
	KindJavaScript Kind = "javascript"
	KindTypeScript Kind = "typescript"
	KindText       Kind = "text"
)

var kindByExtension = map[string]Kind{
	".md": KindMarkdown, ".markdown": KindMarkdown,
	".go": KindGo,
	".py": KindPython,
	".js": KindJavaScript, ".jsx": KindJavaScript, ".mjs": KindJavaScript,
	".ts": KindTypeScript, ".tsx": KindTypeScript,
	".txt": KindText, ".rst": KindText,
}

// KindForPath returns the chunking strategy for a file name.
func KindForPath(p string) (Kind, bool) {
	k, ok := kindByExtension[strings.ToLower(path.Ext(p))]
	return k, ok
}

// ValidKind reports whether k is a supported kind.
func ValidKind(k Kind) bool {
	for _, known := range kindByExtension {
		if known == k {
			return true
		}
	}
	return false
}

// Options bounds chunk sizes in bytes.
//
// all-MiniLM-L6-v2 reads at most 256 word pieces and silently truncates the
// rest, so a chunk larger than the window stores text its embedding never
// saw. Token density differs by kind: on the demo corpus the model's
// tokenizer produced a median of 2.5 bytes/token for Go and Python and 3.5
// for Markdown. Code therefore gets a smaller byte budget than prose.
type Options struct {
	// TargetBytes is the preferred size of prose (Markdown, text) chunks.
	TargetBytes int
	// MaxBytes is a hard limit, below the store's per-row text limit.
	MaxBytes int
	// CodeTargetBytes and CodeMaxBytes apply to source code. Zero means the
	// prose budget.
	CodeTargetBytes int
	CodeMaxBytes    int
	// OverlapLines is repeated between consecutive windows of one oversized
	// block so that a sentence or statement cut at a boundary appears whole
	// in at least one chunk.
	OverlapLines int
}

// DefaultOptions leave room for ~230 content tokens plus the path/section
// header and special tokens at the measured median densities.
var DefaultOptions = Options{TargetBytes: 700, MaxBytes: 900, CodeTargetBytes: 480, CodeMaxBytes: 640, OverlapLines: 2}

// For returns the budget used for kind, with the code budget substituted
// into TargetBytes/MaxBytes for source files.
func (o Options) For(kind Kind) Options {
	switch kind {
	case KindGo, KindPython, KindJavaScript, KindTypeScript:
		if o.CodeTargetBytes > 0 && o.CodeMaxBytes >= o.CodeTargetBytes {
			o.TargetBytes, o.MaxBytes = o.CodeTargetBytes, o.CodeMaxBytes
		}
	}
	return o
}

// Piece is one chunk of a document. Lines are 1-based and inclusive.
type Piece struct {
	Section   string `json:"section"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Text      string `json:"text"`
}

var (
	ErrBinary  = errors.New("file appears to be binary")
	ErrEncoded = errors.New("file is not valid UTF-8")
)

// Sanitize normalizes line endings and removes control characters that carry
// no meaning in source text (keeping tab and newline). NUL bytes mark a file
// as binary. Indentation and all printable characters are preserved, since
// they are syntax in code.
func Sanitize(content []byte) (string, error) {
	if !utf8.Valid(content) {
		return "", ErrEncoded
	}
	s := strings.TrimPrefix(string(content), "\ufeff")
	if strings.IndexByte(s, 0) >= 0 {
		return "", ErrBinary
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r == '\r':
			b.WriteByte('\n')
		case r < 0x20 || r == 0x7f:
			// drop other C0 controls and DEL
		default:
			b.WriteRune(r)
		}
	}
	return b.String(), nil
}

// DocumentID derives a stable identifier from the source name and path, so
// re-ingesting the same file always addresses the same document.
func DocumentID(source, relPath string) string {
	sum := sha256.Sum256([]byte(source + "\x00" + relPath))
	return hex.EncodeToString(sum[:8])
}

// ContentHash identifies file content for incremental ingestion.
func ContentHash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// EmbeddingText is the exact string sent to the encoder for a chunk. The path
// and section header gives the model the file name and symbol or heading,
// which are often the most informative words for code search. Queries are
// embedded without a header.
func EmbeddingText(relPath string, p Piece) string {
	if p.Section == "" {
		return relPath + "\n" + p.Text
	}
	return relPath + " · " + p.Section + "\n" + p.Text
}

// Chunk splits sanitized content according to kind.
func Chunk(kind Kind, relPath, content string, opts Options) []Piece {
	if opts.TargetBytes <= 0 || opts.MaxBytes < opts.TargetBytes {
		opts = DefaultOptions
	}
	opts = opts.For(kind)
	lines := splitLines(content)
	var pieces []Piece
	switch kind {
	case KindMarkdown:
		pieces = chunkMarkdown(lines, opts)
	case KindGo:
		pieces = chunkGo(relPath, content, lines, opts)
	case KindPython:
		pieces = chunkPython(lines, opts)
	case KindJavaScript, KindTypeScript:
		pieces = chunkScript(lines, opts)
	default:
		pieces = packBlocks(paragraphBlocks(lines, 0, len(lines), ""), opts, prosePacking)
	}
	out := pieces[:0]
	for _, p := range pieces {
		p.Text = strings.TrimRight(p.Text, " \t\n")
		if strings.TrimSpace(p.Text) == "" {
			continue
		}
		p.Section = truncateUTF8(p.Section, 256)
		out = append(out, p)
	}
	return out
}

func splitLines(content string) []string {
	content = strings.TrimRight(content, "\n")
	if content == "" {
		return nil
	}
	return strings.Split(content, "\n")
}

// block is a run of lines [start, end) (0-based) that should stay together
// when possible: a paragraph, a code fence or a declaration.
type block struct {
	start, end int
	section    string
	lines      []string // shared slice of all lines
}

func (b block) size() int {
	n := 0
	for _, l := range b.lines[b.start:b.end] {
		n += len(l) + 1
	}
	return n
}

// paragraphBlocks splits lines [start, end) at blank lines.
func paragraphBlocks(lines []string, start, end int, section string) []block {
	var out []block
	i := start
	for i < end {
		for i < end && strings.TrimSpace(lines[i]) == "" {
			i++
		}
		j := i
		for j < end && strings.TrimSpace(lines[j]) != "" {
			j++
		}
		if j > i {
			out = append(out, block{start: i, end: j, section: section, lines: lines})
		}
		i = j
	}
	return out
}

// packMode selects how packBlocks treats section boundaries.
type packMode struct {
	// mergeSections lets small neighbouring declarations share a chunk (code).
	mergeSections bool
	// overlap starts a chunk with the previous chunk's last block when that
	// block is short and both belong to one section (prose), so context that
	// straddles a boundary is retrievable from either side.
	overlap bool
}

var (
	prosePacking = packMode{overlap: true}
	codePacking  = packMode{mergeSections: true}
)

// packBlocks greedily merges consecutive blocks into chunks of at most
// TargetBytes and splits blocks larger than MaxBytes into line windows.
func packBlocks(blocks []block, opts Options, mode packMode) []Piece {
	var out []Piece
	var cur []block
	size, fresh := 0, false // fresh: cur holds a block not yet emitted
	emit := func() {
		if fresh {
			out = append(out, piece(cur))
		}
		cur, size, fresh = nil, 0, false
	}
	for _, b := range blocks {
		bs := b.size()
		if bs > opts.MaxBytes {
			emit()
			out = append(out, splitBlock(b, opts)...)
			continue
		}
		if len(cur) > 0 {
			if !mode.mergeSections && b.section != cur[0].section {
				emit()
			} else if size+bs > opts.TargetBytes {
				last := cur[len(cur)-1]
				emit()
				if mode.overlap && last.size() <= opts.TargetBytes/4 && last.size()+bs <= opts.TargetBytes {
					cur, size = []block{last}, last.size()
				}
			}
		}
		cur = append(cur, b)
		size += bs
		fresh = true
	}
	emit()
	return out
}

func piece(blocks []block) Piece {
	first, last := blocks[0], blocks[len(blocks)-1]
	return Piece{
		Section:   mergedSection(blocks),
		StartLine: first.start + 1,
		EndLine:   last.end,
		Text:      strings.Join(first.lines[first.start:last.end], "\n"),
	}
}

func mergedSection(blocks []block) string {
	var names []string
	seen := map[string]bool{}
	for _, b := range blocks {
		if b.section != "" && !seen[b.section] {
			seen[b.section] = true
			names = append(names, b.section)
		}
	}
	return strings.Join(names, ", ")
}

// splitBlock cuts an oversized block into windows of at most MaxBytes,
// repeating OverlapLines lines between windows. A single line longer than
// MaxBytes is cut at UTF-8 boundaries.
func splitBlock(b block, opts Options) []Piece {
	section := b.section
	var out []Piece
	i := b.start
	part := 1
	for i < b.end {
		size, j := 0, i
		for j < b.end && (j == i || size+len(b.lines[j])+1 <= opts.MaxBytes) {
			size += len(b.lines[j]) + 1
			j++
		}
		text := strings.Join(b.lines[i:j], "\n")
		for len(text) > opts.MaxBytes {
			cut := truncateUTF8(text, opts.MaxBytes)
			out = append(out, Piece{Section: partLabel(section, part), StartLine: i + 1, EndLine: j, Text: cut})
			text = text[len(cut):]
			part++
		}
		out = append(out, Piece{Section: partLabel(section, part), StartLine: i + 1, EndLine: j, Text: text})
		part++
		if j >= b.end {
			break
		}
		next := j - opts.OverlapLines
		if next <= i {
			next = j
		}
		i = next
	}
	return out
}

func partLabel(section string, part int) string {
	if section == "" {
		return "part " + itoa(part)
	}
	return section + " (part " + itoa(part) + ")"
}

func truncateUTF8(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return s[:limit]
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
