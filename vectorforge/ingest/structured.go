package ingest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"regexp"
	"strings"
)

// trimRange shrinks [start, end) to exclude leading and trailing blank lines.
func trimRange(lines []string, start, end int) (int, int) {
	for start < end && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	for end > start && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	return start, end
}

func addBlock(blocks []block, lines []string, start, end int, section string) []block {
	start, end = trimRange(lines, start, end)
	if end <= start {
		return blocks
	}
	return append(blocks, block{start: start, end: end, section: section, lines: lines})
}

var markdownHeading = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*#*\s*$`)

// minSectionBytes is the size below which a Markdown section (typically a
// heading followed directly by its first subsection) joins the next section.
const minSectionBytes = 200

// chunkMarkdown splits at headings, labels each section with its heading
// path ("Durability > Commit protocol"), keeps fenced code blocks intact,
// and packs paragraphs within a section with prose overlap.
func chunkMarkdown(lines []string, opts Options) []Piece {
	type section struct {
		start, end int
		label      string
	}
	var sections []section
	var stack []string
	fence := ""
	current := section{label: ""}
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if fence != "" {
			if strings.HasPrefix(trimmed, fence) {
				fence = ""
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fence = trimmed[:3]
			continue
		}
		m := markdownHeading.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		current.end = i
		sections = append(sections, current)
		level := len(m[1])
		if len(stack) >= level {
			stack = stack[:level-1]
		}
		for len(stack) < level-1 {
			stack = append(stack, "")
		}
		stack = append(stack, strings.TrimSpace(m[2]))
		current = section{start: i, label: joinNonEmpty(stack, " > ")}
	}
	current.end = len(lines)
	sections = append(sections, current)

	// Relabel tiny sections so they pack together with the following one.
	for i := len(sections) - 2; i >= 0; i-- {
		size := 0
		for _, l := range lines[sections[i].start:sections[i].end] {
			size += len(l) + 1
		}
		if size < minSectionBytes {
			sections[i].label = sections[i+1].label
		}
	}

	var blocks []block
	for _, s := range sections {
		blocks = append(blocks, markdownBlocks(lines, s.start, s.end, s.label)...)
	}
	return packBlocks(blocks, opts, prosePacking)
}

// markdownBlocks splits a section into paragraphs, treating a fenced code
// block as one paragraph and attaching a heading to the paragraph after it.
func markdownBlocks(lines []string, start, end int, label string) []block {
	var out []block
	i := start
	for i < end {
		for i < end && strings.TrimSpace(lines[i]) == "" {
			i++
		}
		if i >= end {
			break
		}
		j := i
		fence := ""
		for j < end {
			trimmed := strings.TrimSpace(lines[j])
			if fence != "" {
				if strings.HasPrefix(trimmed, fence) {
					fence = ""
				}
				j++
				continue
			}
			if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
				fence = trimmed[:3]
				j++
				continue
			}
			if trimmed == "" {
				break
			}
			j++
		}
		out = append(out, block{start: i, end: j, section: label, lines: lines})
		i = j
	}
	// A heading-only block merges with the following paragraph of the section.
	merged := out[:0]
	for k := 0; k < len(out); k++ {
		b := out[k]
		if b.end-b.start == 1 && markdownHeading.MatchString(lines[b.start]) && k+1 < len(out) {
			b.end = out[k+1].end
			k++
		}
		merged = append(merged, b)
	}
	return merged
}

func joinNonEmpty(parts []string, sep string) string {
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, sep)
}

// chunkGo uses the standard library parser to cut at top-level declarations.
// Each declaration keeps its doc comment and any loose comments above it;
// the package clause, package doc and imports form a header block. Small
// neighbouring declarations are packed together; a declaration larger than
// MaxBytes is split into overlapping line windows. Files that do not parse
// fall back to paragraph chunking.
func chunkGo(relPath, content string, lines []string, opts Options) []Piece {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, relPath, content, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return packBlocks(paragraphBlocks(lines, 0, len(lines), ""), opts, prosePacking)
	}
	line := func(p token.Pos) int { return fset.Position(p).Line } // 1-based
	headerEnd := line(file.Name.End())
	var blocks []block
	type span struct {
		end   int
		label string
	}
	var decls []span
	for _, decl := range file.Decls {
		if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.IMPORT {
			headerEnd = max(headerEnd, line(gen.End()))
			continue
		}
		decls = append(decls, span{end: line(decl.End()), label: goDeclLabel(decl)})
	}
	blocks = addBlock(blocks, lines, 0, headerEnd, "package "+file.Name.Name)
	prev := headerEnd
	for i, d := range decls {
		end := d.end
		if i == len(decls)-1 {
			end = len(lines) // trailing comments stay with the last declaration
		}
		blocks = addBlock(blocks, lines, prev, end, d.label)
		prev = d.end
	}
	return packBlocks(blocks, opts, codePacking)
}

func goDeclLabel(decl ast.Decl) string {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		if d.Recv != nil && len(d.Recv.List) > 0 {
			recv := d.Recv.List[0]
			name := ""
			if len(recv.Names) > 0 {
				name = recv.Names[0].Name + " "
			}
			return "func (" + name + types.ExprString(recv.Type) + ") " + d.Name.Name
		}
		return "func " + d.Name.Name
	case *ast.GenDecl:
		var names []string
		for _, spec := range d.Specs {
			switch s := spec.(type) {
			case *ast.TypeSpec:
				names = append(names, s.Name.Name)
			case *ast.ValueSpec:
				for _, n := range s.Names {
					names = append(names, n.Name)
				}
			}
		}
		if len(names) > 4 {
			names = append(names[:4], "…")
		}
		return d.Tok.String() + " " + strings.Join(names, ", ")
	}
	return ""
}

var pythonTopLevel = regexp.MustCompile(`^(async\s+def|def|class)\s+(\w+)`)
var pythonMember = regexp.MustCompile(`^(\s+)(async\s+def|def)\s+(\w+)`)

// chunkPython cuts at column-0 def/class statements (with their decorators).
// Classes larger than MaxBytes are further split at method definitions.
// Lines inside triple-quoted strings are never treated as boundaries.
func chunkPython(lines []string, opts Options) []Piece {
	type start struct {
		line  int
		label string
	}
	var starts []start
	inString := ""
	pendingDecorator := -1
	for i, l := range lines {
		if inString != "" {
			if strings.Count(l, inString)%2 == 1 {
				inString = ""
			}
			continue
		}
		if strings.HasPrefix(l, "@") {
			if pendingDecorator < 0 {
				pendingDecorator = i
			}
		} else if m := pythonTopLevel.FindStringSubmatch(l); m != nil {
			first := i
			if pendingDecorator >= 0 {
				first = pendingDecorator
			}
			kind := "def"
			if m[1] == "class" {
				kind = "class"
			}
			starts = append(starts, start{line: first, label: kind + " " + m[2]})
			pendingDecorator = -1
		} else if strings.TrimSpace(l) != "" && !strings.HasPrefix(strings.TrimSpace(l), "#") {
			pendingDecorator = -1
		}
		for _, q := range []string{`"""`, `'''`} {
			if strings.Count(l, q)%2 == 1 {
				inString = q
				break
			}
		}
	}
	var blocks []block
	first := len(lines)
	if len(starts) > 0 {
		first = starts[0].line
	}
	blocks = addBlock(blocks, lines, 0, first, "module")
	for i, s := range starts {
		end := len(lines)
		if i+1 < len(starts) {
			end = starts[i+1].line
		}
		b := block{start: s.line, end: end, section: s.label, lines: lines}
		if strings.HasPrefix(s.label, "class ") && b.size() > opts.MaxBytes {
			blocks = append(blocks, pythonMethods(lines, s.line, end, strings.TrimPrefix(s.label, "class "))...)
			continue
		}
		blocks = addBlock(blocks, lines, s.line, end, s.label)
	}
	return packBlocks(blocks, opts, codePacking)
}

func pythonMethods(lines []string, start, end int, class string) []block {
	var cuts []int
	labels := []string{"class " + class}
	cuts = append(cuts, start)
	for i := start + 1; i < end; i++ {
		if m := pythonMember.FindStringSubmatch(lines[i]); m != nil {
			cut := i
			for cut-1 > start && strings.HasPrefix(strings.TrimSpace(lines[cut-1]), "@") {
				cut--
			}
			cuts = append(cuts, cut)
			labels = append(labels, "class "+class+"."+m[3])
		}
	}
	var out []block
	for i, c := range cuts {
		e := end
		if i+1 < len(cuts) {
			e = cuts[i+1]
		}
		out = addBlock(out, lines, c, e, labels[i])
	}
	return out
}

var scriptTopLevel = regexp.MustCompile(`^(export\s+)?(default\s+)?(declare\s+)?(async\s+)?(function\*?|class|const|let|var|interface|type|enum|abstract\s+class)\b`)

// chunkScript is a heuristic for JavaScript and TypeScript: column-0
// declarations start blocks, which are then packed like code. It does not
// parse the language, so nested structure is not recognized.
func chunkScript(lines []string, opts Options) []Piece {
	var blocks []block
	start, label := 0, "module"
	for i, l := range lines {
		if i > 0 && scriptTopLevel.MatchString(l) {
			blocks = addBlock(blocks, lines, start, i, label)
			start, label = i, scriptLabel(l)
		}
	}
	blocks = addBlock(blocks, lines, start, len(lines), label)
	return packBlocks(blocks, opts, codePacking)
}

func scriptLabel(line string) string {
	label := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(line), "{"))
	return truncateUTF8(label, 80)
}
