package byodb

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"slices"
)

type Result struct {
	Columns      []string
	Rows         []Record
	RowsAffected int
	Message      string
}

func (db *DB) Exec(query string) (*Result, error) {
	stmt, err := Parse(query)
	if err != nil {
		return nil, err
	}
	var tx DBTX
	if err := db.Begin(&tx); err != nil {
		return nil, err
	}
	result, err := executeStatement(&tx, stmt)
	if err != nil {
		db.Abort(&tx)
		return nil, err
	}
	if err := db.Commit(&tx); err != nil {
		return nil, err
	}
	return result, nil
}

// Exec executes a statement inside an existing transaction. The caller owns
// Commit/Abort, allowing multiple statements to be grouped atomically.
func (tx *DBTX) Exec(query string) (*Result, error) {
	if !tx.active {
		return nil, errors.New("inactive database transaction")
	}
	stmt, err := Parse(query)
	if err != nil {
		return nil, err
	}
	return executeStatement(tx, stmt)
}

func executeStatement(tx *DBTX, stmt any) (*Result, error) {
	switch node := stmt.(type) {
	case *QLCreate:
		if err := tx.TableNew(&node.Def); err != nil {
			return nil, err
		}
		return &Result{Message: "table created"}, nil
	case *QLDrop:
		deleted, err := tx.TableDrop(node.Table)
		if err != nil {
			return nil, err
		}
		return &Result{RowsAffected: deleted, Message: fmt.Sprintf("table dropped (%d physical key(s) removed)", deleted)}, nil
	case *QLInsert:
		return executeInsert(tx, node)
	case *QLSelect:
		return executeSelect(tx, node)
	case *QLUpdate:
		return executeUpdate(tx, node)
	case *QLDelete:
		return executeDelete(tx, node)
	default:
		return nil, fmt.Errorf("unsupported statement %T", stmt)
	}
}

func executeInsert(tx *DBTX, stmt *QLInsert) (*Result, error) {
	def := tx.tables[stmt.Table]
	if def == nil {
		return nil, fmt.Errorf("table not found: %s", stmt.Table)
	}
	affected := 0
	for _, expressions := range stmt.Rows {
		if len(expressions) != len(stmt.Cols) {
			return nil, errors.New("INSERT column and value counts do not match")
		}
		rec := Record{Cols: append([]string(nil), stmt.Cols...)}
		for _, expression := range expressions {
			value, err := eval(expression, Record{})
			if err != nil {
				return nil, err
			}
			rec.Vals = append(rec.Vals, value)
		}
		changed, err := tx.Set(stmt.Table, rec, MODE_INSERT_ONLY)
		if err != nil {
			return nil, err
		}
		if !changed {
			return nil, errors.New("INSERT conflicts with an existing primary key")
		}
		affected++
	}
	return &Result{RowsAffected: affected, Message: fmt.Sprintf("%d row(s) inserted", affected)}, nil
}

func executeSelect(tx *DBTX, stmt *QLSelect) (*Result, error) {
	rows, err := scanRows(tx, &stmt.QLScan)
	if err != nil {
		return nil, err
	}
	names := append([]string(nil), stmt.Names...)
	expressions := append([]QLNode(nil), stmt.Output...)
	if len(expressions) == 1 && expressions[0].Type == QL_SYM && string(expressions[0].Str) == "*" {
		def := tx.tables[stmt.Table]
		names = append([]string(nil), def.Cols...)
		expressions = make([]QLNode, len(def.Cols))
		for i, col := range def.Cols {
			expressions[i] = QLNode{Type: QL_SYM, Str: []byte(col)}
		}
	}
	result := &Result{Columns: names}
	for _, row := range rows {
		out := Record{Cols: append([]string(nil), names...)}
		for _, expression := range expressions {
			value, err := eval(expression, row)
			if err != nil {
				return nil, err
			}
			out.Vals = append(out.Vals, value)
		}
		result.Rows = append(result.Rows, out)
	}
	result.RowsAffected = len(result.Rows)
	return result, nil
}

func executeUpdate(tx *DBTX, stmt *QLUpdate) (*Result, error) {
	def := tx.tables[stmt.Table]
	if def == nil {
		return nil, fmt.Errorf("table not found: %s", stmt.Table)
	}
	for _, name := range stmt.Names {
		idx := slices.Index(def.Cols, name)
		if idx < 0 {
			return nil, fmt.Errorf("unknown column: %s", name)
		}
		if idx < def.PKeys {
			return nil, fmt.Errorf("updating primary-key column %q is not supported", name)
		}
	}
	rows, err := scanRows(tx, &stmt.QLScan)
	if err != nil {
		return nil, err
	}
	affected := 0
	for _, original := range rows {
		row := original.Clone()
		values := make([]Value, len(stmt.Values))
		for i, expression := range stmt.Values {
			values[i], err = eval(expression, original)
			if err != nil {
				return nil, err
			}
		}
		for i, name := range stmt.Names {
			idx := slices.Index(row.Cols, name)
			if values[i].Type != def.Types[idx] {
				return nil, fmt.Errorf("type mismatch for column %q", name)
			}
			row.Vals[idx] = values[i]
		}
		changed, err := tx.Set(stmt.Table, row, MODE_UPDATE_ONLY)
		if err != nil {
			return nil, err
		}
		if changed {
			affected++
		}
	}
	return &Result{RowsAffected: affected, Message: fmt.Sprintf("%d row(s) updated", affected)}, nil
}

func executeDelete(tx *DBTX, stmt *QLDelete) (*Result, error) {
	def := tx.tables[stmt.Table]
	if def == nil {
		return nil, fmt.Errorf("table not found: %s", stmt.Table)
	}
	rows, err := scanRows(tx, &stmt.QLScan)
	if err != nil {
		return nil, err
	}
	affected := 0
	for _, row := range rows {
		pk := Record{Cols: append([]string(nil), def.Cols[:def.PKeys]...), Vals: append([]Value(nil), row.Vals[:def.PKeys]...)}
		changed, err := tx.Delete(stmt.Table, pk)
		if err != nil {
			return nil, err
		}
		if changed {
			affected++
		}
	}
	return &Result{RowsAffected: affected, Message: fmt.Sprintf("%d row(s) deleted", affected)}, nil
}

func scanRows(tx *DBTX, req *QLScan) ([]Record, error) {
	scanner, err := scannerFromQL(req)
	if err != nil {
		return nil, err
	}
	if err := tx.Scan(req.Table, scanner); err != nil {
		return nil, err
	}
	rows := []Record{}
	var matched int64
	for scanner.Valid() {
		var row Record
		if err := scanner.Deref(&row); err != nil {
			return nil, err
		}
		keep := true
		if req.Filter.Type != 0 {
			value, err := eval(req.Filter, row)
			if err != nil {
				return nil, err
			}
			if value.Type != TYPE_BOOL {
				return nil, errors.New("FILTER expression must be boolean")
			}
			keep = value.Bool
		}
		if keep {
			if matched >= req.Offset && int64(len(rows)) < req.Limit {
				rows = append(rows, row)
			}
			matched++
			if int64(len(rows)) >= req.Limit {
				break
			}
		}
		scanner.Next()
	}
	return rows, nil
}

func scannerFromQL(req *QLScan) (*Scanner, error) {
	sc := &Scanner{}
	if req.IndexBy.Type == 0 {
		return sc, nil
	}
	comparisons := []QLNode{}
	var flatten func(QLNode)
	flatten = func(node QLNode) {
		if node.Type == QL_AND {
			flatten(node.Kids[0])
			flatten(node.Kids[1])
		} else {
			comparisons = append(comparisons, node)
		}
	}
	flatten(req.IndexBy)
	if len(comparisons) == 0 || len(comparisons) > 2 {
		return nil, errors.New("INDEX BY supports one comparison or a two-sided range")
	}
	for i, comparison := range comparisons {
		rec, cmp, err := scanComparison(comparison)
		if err != nil {
			return nil, err
		}
		if comparison.Type == QL_EQ {
			sc.Key1, sc.Key2 = rec, rec.Clone()
			sc.Cmp1, sc.Cmp2 = CMP_GE, CMP_LE
			if len(comparisons) != 1 {
				return nil, errors.New("equality cannot be combined with another INDEX BY bound")
			}
			return sc, nil
		}
		if i == 0 {
			sc.Key1, sc.Cmp1 = rec, cmp
		} else {
			sc.Key2, sc.Cmp2 = rec, cmp
		}
	}
	return sc, nil
}

func scanComparison(node QLNode) (Record, int, error) {
	if node.Type < QL_EQ || node.Type > QL_GE || len(node.Kids) != 2 || node.Type == QL_NE {
		return Record{}, 0, errors.New("INDEX BY requires =, <, <=, >, or >=")
	}
	cols, leftOK := symbolTuple(node.Kids[0])
	values, rightOK, err := constantTuple(node.Kids[1])
	if err != nil {
		return Record{}, 0, err
	}
	reversed := false
	if !leftOK || !rightOK {
		cols, leftOK = symbolTuple(node.Kids[1])
		values, rightOK, err = constantTuple(node.Kids[0])
		if err != nil {
			return Record{}, 0, err
		}
		reversed = true
	}
	if !leftOK || !rightOK || len(cols) != len(values) {
		return Record{}, 0, errors.New("INDEX BY compares column tuple to constant tuple")
	}
	typ := node.Type
	if reversed {
		switch typ {
		case QL_LT:
			typ = QL_GT
		case QL_LE:
			typ = QL_GE
		case QL_GT:
			typ = QL_LT
		case QL_GE:
			typ = QL_LE
		}
	}
	cmp := map[uint32]int{QL_EQ: CMP_GE, QL_LT: CMP_LT, QL_LE: CMP_LE, QL_GT: CMP_GT, QL_GE: CMP_GE}[typ]
	return Record{Cols: cols, Vals: values}, cmp, nil
}

func symbolTuple(node QLNode) ([]string, bool) {
	if node.Type == QL_SYM {
		return []string{string(node.Str)}, true
	}
	if node.Type != QL_TUPLE {
		return nil, false
	}
	out := make([]string, len(node.Kids))
	for i, kid := range node.Kids {
		if kid.Type != QL_SYM {
			return nil, false
		}
		out[i] = string(kid.Str)
	}
	return out, true
}

func constantTuple(node QLNode) ([]Value, bool, error) {
	if node.Type == QL_TUPLE {
		out := make([]Value, len(node.Kids))
		for i, kid := range node.Kids {
			value, err := eval(kid, Record{})
			if err != nil {
				return nil, false, nil
			}
			out[i] = value
		}
		return out, true, nil
	}
	value, err := eval(node, Record{})
	if err != nil {
		return nil, false, nil
	}
	return []Value{value}, true, nil
}

func eval(node QLNode, env Record) (Value, error) {
	switch node.Type {
	case QL_SYM:
		value := env.Get(string(node.Str))
		if value == nil {
			return Value{}, fmt.Errorf("unknown column: %s", node.Str)
		}
		out := *value
		out.Str = cloneBytes(out.Str)
		return out, nil
	case QL_I64:
		return Int64Value(node.I64), nil
	case QL_STR:
		return BytesValue(node.Str), nil
	case QL_BOOL:
		return BoolValue(node.Bool), nil
	case QL_NEG:
		value, err := eval(node.Kids[0], env)
		if err != nil || value.Type != TYPE_INT64 {
			return Value{}, expressionTypeError(node.Type, err)
		}
		if value.I64 == math.MinInt64 {
			return Value{}, errors.New("integer overflow")
		}
		return Int64Value(-value.I64), nil
	case QL_NOT:
		value, err := eval(node.Kids[0], env)
		if err != nil || value.Type != TYPE_BOOL {
			return Value{}, expressionTypeError(node.Type, err)
		}
		return BoolValue(!value.Bool), nil
	case QL_AND, QL_OR:
		left, err := eval(node.Kids[0], env)
		if err != nil || left.Type != TYPE_BOOL {
			return Value{}, expressionTypeError(node.Type, err)
		}
		if node.Type == QL_AND && !left.Bool {
			return BoolValue(false), nil
		}
		if node.Type == QL_OR && left.Bool {
			return BoolValue(true), nil
		}
		right, err := eval(node.Kids[1], env)
		if err != nil || right.Type != TYPE_BOOL {
			return Value{}, expressionTypeError(node.Type, err)
		}
		if node.Type == QL_AND {
			return BoolValue(left.Bool && right.Bool), nil
		}
		return BoolValue(left.Bool || right.Bool), nil
	case QL_ADD, QL_SUB, QL_MUL, QL_DIV:
		left, err := eval(node.Kids[0], env)
		if err != nil {
			return Value{}, err
		}
		right, err := eval(node.Kids[1], env)
		if err != nil {
			return Value{}, err
		}
		if node.Type == QL_ADD && left.Type == TYPE_BYTES && right.Type == TYPE_BYTES {
			return BytesValue(append(cloneBytes(left.Str), right.Str...)), nil
		}
		if left.Type != TYPE_INT64 || right.Type != TYPE_INT64 {
			return Value{}, expressionTypeError(node.Type, nil)
		}
		switch node.Type {
		case QL_ADD:
			return Int64Value(left.I64 + right.I64), nil
		case QL_SUB:
			return Int64Value(left.I64 - right.I64), nil
		case QL_MUL:
			return Int64Value(left.I64 * right.I64), nil
		default:
			if right.I64 == 0 {
				return Value{}, errors.New("division by zero")
			}
			return Int64Value(left.I64 / right.I64), nil
		}
	case QL_EQ, QL_NE, QL_LT, QL_LE, QL_GT, QL_GE:
		left, err := eval(node.Kids[0], env)
		if err != nil {
			return Value{}, err
		}
		right, err := eval(node.Kids[1], env)
		if err != nil {
			return Value{}, err
		}
		cmp, err := compareValues(left, right)
		if err != nil {
			return Value{}, err
		}
		switch node.Type {
		case QL_EQ:
			return BoolValue(cmp == 0), nil
		case QL_NE:
			return BoolValue(cmp != 0), nil
		case QL_LT:
			return BoolValue(cmp < 0), nil
		case QL_LE:
			return BoolValue(cmp <= 0), nil
		case QL_GT:
			return BoolValue(cmp > 0), nil
		default:
			return BoolValue(cmp >= 0), nil
		}
	default:
		return Value{}, fmt.Errorf("unsupported expression node %d", node.Type)
	}
}

func compareValues(a, b Value) (int, error) {
	if a.Type != b.Type {
		return 0, errors.New("cannot compare values of different types")
	}
	switch a.Type {
	case TYPE_BYTES:
		return bytes.Compare(a.Str, b.Str), nil
	case TYPE_INT64:
		if a.I64 < b.I64 {
			return -1, nil
		}
		if a.I64 > b.I64 {
			return 1, nil
		}
		return 0, nil
	case TYPE_BOOL:
		if a.Bool == b.Bool {
			return 0, nil
		}
		if !a.Bool {
			return -1, nil
		}
		return 1, nil
	default:
		return 0, errors.New("invalid value type")
	}
}

func expressionTypeError(operator uint32, nested error) error {
	if nested != nil {
		return nested
	}
	return fmt.Errorf("type error for expression operator %d", operator)
}
