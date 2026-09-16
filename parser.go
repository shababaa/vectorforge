package byodb

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	QL_SYM uint32 = iota + 1
	QL_I64
	QL_STR
	QL_BOOL
	QL_TUPLE
	QL_OR
	QL_AND
	QL_NOT
	QL_EQ
	QL_NE
	QL_LT
	QL_LE
	QL_GT
	QL_GE
	QL_ADD
	QL_SUB
	QL_MUL
	QL_DIV
	QL_NEG
)

type QLNode struct {
	Type uint32
	I64  int64
	Str  []byte
	Bool bool
	Kids []QLNode
}

type QLScan struct {
	Table   string
	IndexBy QLNode
	Filter  QLNode
	Offset  int64
	Limit   int64
}

type QLSelect struct {
	QLScan
	Names  []string
	Output []QLNode
}

type QLInsert struct {
	Table string
	Cols  []string
	Rows  [][]QLNode
}

type QLUpdate struct {
	QLScan
	Names  []string
	Values []QLNode
}

type QLDelete struct{ QLScan }
type QLCreate struct{ Def TableDef }
type QLDrop struct{ Table string }

type tokenKind uint8

const (
	tokEOF tokenKind = iota
	tokIdent
	tokNumber
	tokString
	tokPunct
)

type token struct {
	kind tokenKind
	text string
	pos  int
}

type parseFailure struct{ err error }

type Parser struct {
	input  string
	tokens []token
	pos    int
}

func Parse(input string) (stmt any, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if failure, ok := recovered.(parseFailure); ok {
				stmt, err = nil, failure.err
				return
			}
			panic(recovered)
		}
	}()
	tokens, err := lex(input)
	if err != nil {
		return nil, err
	}
	p := &Parser{input: input, tokens: tokens}
	stmt = p.statement()
	p.match(";")
	if p.peek().kind != tokEOF {
		p.fail("unexpected token %q", p.peek().text)
	}
	return stmt, nil
}

func lex(input string) ([]token, error) {
	out := []token{}
	for pos := 0; pos < len(input); {
		r, size := utf8.DecodeRuneInString(input[pos:])
		if unicode.IsSpace(r) {
			pos += size
			continue
		}
		if strings.HasPrefix(input[pos:], "--") {
			if end := strings.IndexByte(input[pos:], '\n'); end >= 0 {
				pos += end + 1
			} else {
				pos = len(input)
			}
			continue
		}
		start := pos
		if unicode.IsLetter(r) || r == '_' || r == '@' {
			pos += size
			for pos < len(input) {
				r, size = utf8.DecodeRuneInString(input[pos:])
				if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '@' {
					break
				}
				pos += size
			}
			out = append(out, token{kind: tokIdent, text: input[start:pos], pos: start})
			continue
		}
		if unicode.IsDigit(r) {
			pos += size
			for pos < len(input) && input[pos] >= '0' && input[pos] <= '9' {
				pos++
			}
			out = append(out, token{kind: tokNumber, text: input[start:pos], pos: start})
			continue
		}
		if r == '\'' || r == '"' {
			quote := byte(r)
			pos += size
			var value strings.Builder
			for pos < len(input) {
				if input[pos] == quote {
					if pos+1 < len(input) && input[pos+1] == quote {
						value.WriteByte(quote)
						pos += 2
						continue
					}
					pos++
					out = append(out, token{kind: tokString, text: value.String(), pos: start})
					goto nextToken
				}
				if input[pos] == '\\' && pos+1 < len(input) {
					pos++
					switch input[pos] {
					case 'n':
						value.WriteByte('\n')
					case 'r':
						value.WriteByte('\r')
					case 't':
						value.WriteByte('\t')
					default:
						value.WriteByte(input[pos])
					}
					pos++
					continue
				}
				value.WriteByte(input[pos])
				pos++
			}
			return nil, fmt.Errorf("unterminated string at byte %d", start)
		}
		if pos+1 < len(input) {
			op := input[pos : pos+2]
			if op == "<=" || op == ">=" || op == "!=" || op == "<>" || op == "==" {
				out = append(out, token{kind: tokPunct, text: op, pos: pos})
				pos += 2
				continue
			}
		}
		if strings.ContainsRune("(),;=<>+-*/", r) {
			out = append(out, token{kind: tokPunct, text: string(r), pos: pos})
			pos += size
			continue
		}
		return nil, fmt.Errorf("invalid character %q at byte %d", r, pos)
	nextToken:
		continue
	}
	out = append(out, token{kind: tokEOF, pos: len(input)})
	return out, nil
}

func (p *Parser) peek() token { return p.tokens[p.pos] }

func (p *Parser) match(parts ...string) bool {
	if p.pos+len(parts) > len(p.tokens) {
		return false
	}
	for i, part := range parts {
		if !strings.EqualFold(p.tokens[p.pos+i].text, part) {
			return false
		}
	}
	p.pos += len(parts)
	return true
}

func (p *Parser) keyword(words ...string) bool { return p.match(words...) }

func (p *Parser) expect(part string) {
	if !p.match(part) {
		p.fail("expected %q, got %q", part, p.peek().text)
	}
}

func (p *Parser) ident() string {
	t := p.peek()
	if t.kind != tokIdent {
		p.fail("expected a name, got %q", t.text)
	}
	p.pos++
	return t.text
}

func (p *Parser) fail(format string, args ...any) {
	panic(parseFailure{err: fmt.Errorf("parse error at byte %d: %s", p.peek().pos, fmt.Sprintf(format, args...))})
}

func (p *Parser) statement() any {
	switch {
	case p.keyword("create", "table"):
		return p.createTable()
	case p.keyword("drop", "table"):
		return &QLDrop{Table: p.ident()}
	case p.keyword("select"):
		return p.selectStmt()
	case p.keyword("insert", "into"):
		return p.insertStmt()
	case p.keyword("update"):
		return p.updateStmt()
	case p.keyword("delete", "from"):
		return p.deleteStmt()
	default:
		p.fail("expected CREATE, DROP, SELECT, INSERT, UPDATE, or DELETE")
		return nil
	}
}

func (p *Parser) createTable() *QLCreate {
	name := p.ident()
	p.expect("(")
	type column struct {
		name string
		typ  uint32
	}
	columns := []column{}
	indexes := [][]string{}
	var primary []string
	for {
		switch {
		case p.keyword("primary", "key"):
			primary = p.nameList()
		case p.keyword("index"):
			if p.peek().text != "(" {
				_ = p.ident() // optional index name
			}
			indexes = append(indexes, p.nameList())
		default:
			col := column{name: p.ident()}
			switch strings.ToLower(p.ident()) {
			case "string", "bytes", "text":
				col.typ = TYPE_BYTES
			case "int", "int64", "integer":
				col.typ = TYPE_INT64
			default:
				p.fail("unknown column type")
			}
			columns = append(columns, col)
		}
		if p.match(")") {
			break
		}
		p.expect(",")
		if p.match(")") { // trailing comma
			break
		}
	}
	if len(primary) == 0 {
		p.fail("CREATE TABLE requires PRIMARY KEY")
	}
	byName := map[string]column{}
	for _, col := range columns {
		byName[col.name] = col
	}
	ordered := []column{}
	used := map[string]bool{}
	for _, name := range primary {
		col, ok := byName[name]
		if !ok || used[name] {
			p.fail("invalid primary-key column %q", name)
		}
		ordered, used[name] = append(ordered, col), true
	}
	for _, col := range columns {
		if !used[col.name] {
			ordered = append(ordered, col)
		}
	}
	def := TableDef{Name: name, PKeys: len(primary), Indexes: append([][]string{primary}, indexes...)}
	for _, col := range ordered {
		def.Cols = append(def.Cols, col.name)
		def.Types = append(def.Types, col.typ)
	}
	return &QLCreate{Def: def}
}

func (p *Parser) nameList() []string {
	p.expect("(")
	out := []string{p.ident()}
	for p.match(",") {
		out = append(out, p.ident())
	}
	p.expect(")")
	return out
}

func (p *Parser) selectStmt() *QLSelect {
	stmt := &QLSelect{}
	for i := 0; ; i++ {
		expr := p.exprOr()
		name := ""
		if p.keyword("as") {
			name = p.ident()
		} else if expr.Type == QL_SYM {
			name = string(expr.Str)
		} else {
			name = fmt.Sprintf("expr%d", i+1)
		}
		stmt.Output, stmt.Names = append(stmt.Output, expr), append(stmt.Names, name)
		if !p.match(",") {
			break
		}
	}
	if !p.keyword("from") {
		p.fail("expected FROM")
	}
	stmt.Table = p.ident()
	p.scanClauses(&stmt.QLScan)
	return stmt
}

func (p *Parser) insertStmt() *QLInsert {
	stmt := &QLInsert{Table: p.ident()}
	stmt.Cols = p.nameList()
	if !p.keyword("values") {
		p.fail("expected VALUES")
	}
	for {
		p.expect("(")
		row := []QLNode{p.exprOr()}
		for p.match(",") {
			row = append(row, p.exprOr())
		}
		p.expect(")")
		stmt.Rows = append(stmt.Rows, row)
		if !p.match(",") {
			break
		}
	}
	return stmt
}

func (p *Parser) updateStmt() *QLUpdate {
	stmt := &QLUpdate{}
	stmt.Table = p.ident()
	if !p.keyword("set") {
		p.fail("expected SET")
	}
	for {
		stmt.Names = append(stmt.Names, p.ident())
		p.expect("=")
		stmt.Values = append(stmt.Values, p.exprOr())
		if !p.match(",") {
			break
		}
	}
	p.scanClauses(&stmt.QLScan)
	return stmt
}

func (p *Parser) deleteStmt() *QLDelete {
	stmt := &QLDelete{}
	stmt.Table = p.ident()
	p.scanClauses(&stmt.QLScan)
	return stmt
}

func (p *Parser) scanClauses(scan *QLScan) {
	if p.keyword("index", "by") {
		scan.IndexBy = p.exprOr()
	}
	if p.keyword("filter") || p.keyword("where") {
		scan.Filter = p.exprOr()
	}
	scan.Limit = math.MaxInt64
	if p.keyword("limit") {
		first := p.number()
		if p.match(",") {
			scan.Offset, scan.Limit = first, p.number()
		} else {
			scan.Limit = first
		}
		if scan.Offset < 0 || scan.Limit < 0 {
			p.fail("LIMIT values must be non-negative")
		}
	}
}

func (p *Parser) number() int64 {
	t := p.peek()
	if t.kind != tokNumber {
		p.fail("expected a number")
	}
	p.pos++
	v, err := strconv.ParseInt(t.text, 10, 64)
	if err != nil {
		p.fail("invalid integer %q", t.text)
	}
	return v
}

func binaryNode(kind uint32, left, right QLNode) QLNode {
	return QLNode{Type: kind, Kids: []QLNode{left, right}}
}

func (p *Parser) exprOr() QLNode {
	n := p.exprAnd()
	for p.keyword("or") {
		n = binaryNode(QL_OR, n, p.exprAnd())
	}
	return n
}

func (p *Parser) exprAnd() QLNode {
	n := p.exprNot()
	for p.keyword("and") {
		n = binaryNode(QL_AND, n, p.exprNot())
	}
	return n
}

func (p *Parser) exprNot() QLNode {
	if p.keyword("not") {
		return QLNode{Type: QL_NOT, Kids: []QLNode{p.exprNot()}}
	}
	return p.exprCmp()
}

func (p *Parser) exprCmp() QLNode {
	n := p.exprAdd()
	var kind uint32
	switch {
	case p.match("=", "=") || p.match("==") || p.match("="):
		kind = QL_EQ
	case p.match("!=") || p.match("<>"):
		kind = QL_NE
	case p.match("<="):
		kind = QL_LE
	case p.match(">="):
		kind = QL_GE
	case p.match("<"):
		kind = QL_LT
	case p.match(">"):
		kind = QL_GT
	default:
		return n
	}
	return binaryNode(kind, n, p.exprAdd())
}

func (p *Parser) exprAdd() QLNode {
	n := p.exprMul()
	for {
		switch {
		case p.match("+"):
			n = binaryNode(QL_ADD, n, p.exprMul())
		case p.match("-"):
			n = binaryNode(QL_SUB, n, p.exprMul())
		default:
			return n
		}
	}
}

func (p *Parser) exprMul() QLNode {
	n := p.exprUnary()
	for {
		switch {
		case p.match("*"):
			n = binaryNode(QL_MUL, n, p.exprUnary())
		case p.match("/"):
			n = binaryNode(QL_DIV, n, p.exprUnary())
		default:
			return n
		}
	}
}

func (p *Parser) exprUnary() QLNode {
	if p.match("-") {
		return QLNode{Type: QL_NEG, Kids: []QLNode{p.exprUnary()}}
	}
	if p.match("+") {
		return p.exprUnary()
	}
	return p.exprAtom()
}

func (p *Parser) exprAtom() QLNode {
	t := p.peek()
	if p.match("*") {
		return QLNode{Type: QL_SYM, Str: []byte("*")}
	}
	switch t.kind {
	case tokIdent:
		p.pos++
		if strings.EqualFold(t.text, "true") || strings.EqualFold(t.text, "false") {
			return QLNode{Type: QL_BOOL, Bool: strings.EqualFold(t.text, "true")}
		}
		return QLNode{Type: QL_SYM, Str: []byte(t.text)}
	case tokNumber:
		return QLNode{Type: QL_I64, I64: p.number()}
	case tokString:
		p.pos++
		return QLNode{Type: QL_STR, Str: []byte(t.text)}
	}
	if p.match("(") {
		first := p.exprOr()
		if !p.match(",") {
			p.expect(")")
			return first
		}
		items := []QLNode{first, p.exprOr()}
		for p.match(",") {
			items = append(items, p.exprOr())
		}
		p.expect(")")
		return QLNode{Type: QL_TUPLE, Kids: items}
	}
	p.fail("expected expression, got %q", t.text)
	return QLNode{}
}
