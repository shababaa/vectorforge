package byodb

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
)

type TableDef struct {
	Name     string     `json:"name"`
	Types    []uint32   `json:"types"`
	Cols     []string   `json:"cols"`
	PKeys    int        `json:"pkeys,omitempty"`
	Indexes  [][]string `json:"indexes"`
	Prefixes []uint32   `json:"prefixes"`
}

var TDEF_META = &TableDef{
	Name: "@meta", Types: []uint32{TYPE_BYTES, TYPE_BYTES},
	Cols: []string{"key", "val"}, PKeys: 1,
	Indexes: [][]string{{"key"}}, Prefixes: []uint32{1},
}

var TDEF_TABLE = &TableDef{
	Name: "@table", Types: []uint32{TYPE_BYTES, TYPE_BYTES},
	Cols: []string{"name", "def"}, PKeys: 1,
	Indexes: [][]string{{"name"}}, Prefixes: []uint32{2},
}

const (
	// MaxDeleteRangeKeys bounds each physical key batch held temporarily while
	// deleting a range. Larger logical deletes proceed through repeated batches
	// in the same transaction.
	MaxDeleteRangeKeys = 4096
	// DefaultDeleteRangeRows keeps the public relational operation predictable.
	DefaultDeleteRangeRows = 1000
	MaxDeleteRangeRows     = 10_000
)

type DB struct {
	Path string

	mu      sync.RWMutex
	kv      *KV
	tables  map[string]*TableDef
	options DBOptions
}

// DBOptions controls how the embedded relational database is opened.
type DBOptions struct {
	ReadOnly bool
}

func OpenDB(path string) (*DB, error) {
	return OpenDBWithOptions(path, DBOptions{})
}

// OpenDBReadOnly opens an existing database with a shared process lock. It can
// coexist with other read-only handles, but not with the database writer.
func OpenDBReadOnly(path string) (*DB, error) {
	return OpenDBWithOptions(path, DBOptions{ReadOnly: true})
}

func OpenDBWithOptions(path string, options DBOptions) (*DB, error) {
	db := &DB{Path: path, options: options}
	if err := db.Open(); err != nil {
		return nil, err
	}
	return db, nil
}

func (db *DB) Open() error {
	if db.Path == "" {
		return errors.New("database path is empty")
	}
	kv, err := OpenKVWithOptions(db.Path, KVOptions{ReadOnly: db.options.ReadOnly})
	if err != nil {
		return err
	}
	db.kv = kv
	db.tables = map[string]*TableDef{
		TDEF_META.Name:  cloneTableDef(TDEF_META),
		TDEF_TABLE.Name: cloneTableDef(TDEF_TABLE),
	}
	if err := db.loadTableDefs(); err != nil {
		kv.Close()
		return err
	}
	return nil
}

// ReadOnly reports whether this handle permits commits containing writes.
func (db *DB) ReadOnly() bool { return db.options.ReadOnly }

// Backup creates a durable point-in-time copy suitable for a read-only process.
func (db *DB) Backup(path string) error {
	if db.kv == nil {
		return ErrClosed
	}
	return db.kv.Backup(path)
}

func (db *DB) Close() error {
	if db.kv == nil {
		return nil
	}
	return db.kv.Close()
}

// Table returns a defensive copy of a table definition. It gives embedded
// applications enough schema introspection to implement migrations without
// exposing the database's mutable catalog.
func (db *DB) Table(name string) (*TableDef, bool) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	def, ok := db.tables[name]
	return cloneTableDef(def), ok
}

// Tables returns defensive copies of all table definitions, ordered by name.
func (db *DB) Tables() []*TableDef {
	db.mu.RLock()
	defer db.mu.RUnlock()
	names := make([]string, 0, len(db.tables))
	for name := range db.tables {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]*TableDef, 0, len(names))
	for _, name := range names {
		out = append(out, cloneTableDef(db.tables[name]))
	}
	return out
}

type DBTX struct {
	db            *DB
	kv            KVTX
	tables        map[string]*TableDef
	schemaChanges map[string]*TableDef
	active        bool
}

// Table returns the transaction's view of a table definition. Newly created
// tables are visible immediately inside the transaction.
func (tx *DBTX) Table(name string) (*TableDef, bool) {
	if !tx.active {
		return nil, false
	}
	def, ok := tx.tables[name]
	return cloneTableDef(def), ok
}

func (db *DB) Begin(tx *DBTX) error {
	if db.kv == nil {
		return ErrClosed
	}
	if err := db.kv.Begin(&tx.kv); err != nil {
		return err
	}
	tx.db, tx.active = db, true
	tx.tables = map[string]*TableDef{}
	tx.schemaChanges = map[string]*TableDef{}
	db.mu.RLock()
	for name, def := range db.tables {
		tx.tables[name] = cloneTableDef(def)
	}
	db.mu.RUnlock()
	return nil
}

func (db *DB) Commit(tx *DBTX) error {
	if !tx.active || tx.db != db {
		return errors.New("inactive database transaction")
	}
	err := db.kv.Commit(&tx.kv)
	tx.active = false
	if err != nil {
		return err
	}
	if len(tx.schemaChanges) > 0 {
		db.mu.Lock()
		for name, def := range tx.schemaChanges {
			if def == nil {
				delete(db.tables, name)
			} else {
				db.tables[name] = cloneTableDef(def)
			}
		}
		db.mu.Unlock()
	}
	return nil
}

func (db *DB) Abort(tx *DBTX) {
	if tx.active && tx.db == db {
		db.kv.Abort(&tx.kv)
		tx.active = false
	}
}

func (db *DB) loadTableDefs() error {
	var tx DBTX
	if err := db.Begin(&tx); err != nil {
		return err
	}
	defer db.Abort(&tx)
	sc := Scanner{}
	if err := tx.Scan(TDEF_TABLE.Name, &sc); err != nil {
		return err
	}
	for sc.Valid() {
		var rec Record
		if err := sc.Deref(&rec); err != nil {
			return err
		}
		def := &TableDef{}
		if err := json.Unmarshal(rec.Get("def").Str, def); err != nil {
			return fmt.Errorf("decode table %q: %w", rec.Get("name").Str, err)
		}
		if err := validateTableDef(def, true); err != nil {
			return err
		}
		db.tables[def.Name] = def
		sc.Next()
	}
	return nil
}

func cloneTableDef(in *TableDef) *TableDef {
	if in == nil {
		return nil
	}
	out := *in
	out.Types = append([]uint32(nil), in.Types...)
	out.Cols = append([]string(nil), in.Cols...)
	out.Prefixes = append([]uint32(nil), in.Prefixes...)
	out.Indexes = make([][]string, len(in.Indexes))
	for i := range in.Indexes {
		out.Indexes[i] = append([]string(nil), in.Indexes[i]...)
	}
	return &out
}

func validateTableDef(def *TableDef, assigned bool) error {
	if def == nil || def.Name == "" || def.Name[0] == '@' {
		if def != nil && (def.Name == TDEF_META.Name || def.Name == TDEF_TABLE.Name) {
			return nil
		}
		return errors.New("table name is empty or reserved")
	}
	if len(def.Cols) == 0 || len(def.Cols) != len(def.Types) {
		return errors.New("table columns and types do not match")
	}
	seen := map[string]bool{}
	for i, col := range def.Cols {
		if col == "" || seen[col] {
			return fmt.Errorf("invalid or duplicate column %q", col)
		}
		seen[col] = true
		if def.Types[i] != TYPE_BYTES && def.Types[i] != TYPE_INT64 {
			return fmt.Errorf("unsupported type for column %q", col)
		}
	}
	if def.PKeys <= 0 || def.PKeys > len(def.Cols) {
		return errors.New("table must have at least one primary-key column")
	}
	if len(def.Indexes) == 0 || !slices.Equal(def.Indexes[0], def.Cols[:def.PKeys]) {
		return errors.New("the first index must be the primary key")
	}
	for _, index := range def.Indexes {
		if len(index) == 0 {
			return errors.New("empty index")
		}
		idxSeen := map[string]bool{}
		for _, col := range index {
			if !seen[col] || idxSeen[col] {
				return fmt.Errorf("invalid indexed column %q", col)
			}
			idxSeen[col] = true
		}
	}
	if assigned && len(def.Prefixes) != len(def.Indexes) {
		return errors.New("table prefixes do not match indexes")
	}
	return nil
}

func normalizeNewTable(def *TableDef) (*TableDef, error) {
	out := cloneTableDef(def)
	if out == nil || out.PKeys < 0 || out.PKeys > len(out.Cols) {
		return nil, errors.New("invalid primary-key column count")
	}
	if out.PKeys == 0 && len(out.Indexes) > 0 {
		out.PKeys = len(out.Indexes[0])
	}
	if out.PKeys <= 0 || out.PKeys > len(out.Cols) {
		return nil, errors.New("invalid primary-key column count")
	}
	primary := append([]string(nil), out.Cols[:out.PKeys]...)
	if len(out.Indexes) == 0 || !slices.Equal(out.Indexes[0], primary) {
		out.Indexes = append([][]string{primary}, out.Indexes...)
	}
	out.Prefixes = nil
	if err := validateTableDef(out, false); err != nil {
		return nil, err
	}
	return out, nil
}

func (tx *DBTX) TableNew(input *TableDef) error {
	def, err := normalizeNewTable(input)
	if err != nil {
		return err
	}
	if tx.tables[def.Name] != nil {
		return fmt.Errorf("table already exists: %s", def.Name)
	}
	next := uint32(3)
	meta := (&Record{}).AddString("key", "next_prefix")
	if ok, err := tx.get(TDEF_META, meta); err != nil {
		return err
	} else if ok {
		if len(meta.Get("val").Str) != 4 {
			return errors.New("invalid next_prefix metadata")
		}
		next = binary.BigEndian.Uint32(meta.Get("val").Str)
	}
	def.Prefixes = make([]uint32, len(def.Indexes))
	for i := range def.Prefixes {
		def.Prefixes[i] = next
		next++
	}
	var nextBytes [4]byte
	binary.BigEndian.PutUint32(nextBytes[:], next)
	meta = (&Record{}).AddString("key", "next_prefix").AddStr("val", nextBytes[:])
	if _, err := tx.set(TDEF_META, *meta, MODE_UPSERT); err != nil {
		return err
	}
	encoded, err := json.Marshal(def)
	if err != nil {
		return err
	}
	catalog := (&Record{}).AddString("name", def.Name).AddStr("def", encoded)
	if _, err := tx.set(TDEF_TABLE, *catalog, MODE_INSERT_ONLY); err != nil {
		return err
	}
	tx.tables[def.Name] = def
	tx.schemaChanges[def.Name] = def
	return nil
}

func (db *DB) TableNew(def *TableDef) error {
	var tx DBTX
	if err := db.Begin(&tx); err != nil {
		return err
	}
	if err := tx.TableNew(def); err != nil {
		db.Abort(&tx)
		return err
	}
	return db.Commit(&tx)
}

// TableDrop atomically removes a table's primary rows, secondary-index keys,
// and catalog record. Prefixes are never reused, so stale keys cannot become
// visible as a different table after a crash or later CREATE.
func (tx *DBTX) TableDrop(name string) (int, error) {
	def := tx.tables[name]
	if def == nil {
		return 0, fmt.Errorf("table not found: %s", name)
	}
	if name == TDEF_META.Name || name == TDEF_TABLE.Name {
		return 0, fmt.Errorf("cannot drop reserved table: %s", name)
	}
	deleted := 0
	for _, prefix := range def.Prefixes {
		start := prefixKey(prefix)
		stop := keySuccessor(start)
		for {
			count, more, err := tx.kv.DeleteRange(start, stop, MaxDeleteRangeKeys)
			if err != nil {
				return deleted, err
			}
			deleted += count
			if !more {
				break
			}
		}
	}
	catalog := *(&Record{}).AddString("name", name)
	if changed, err := tx.Delete(TDEF_TABLE.Name, catalog); err != nil {
		return deleted, err
	} else if !changed {
		return deleted, fmt.Errorf("table catalog record is missing: %s", name)
	}
	delete(tx.tables, name)
	tx.schemaChanges[name] = nil
	return deleted, nil
}

func (db *DB) TableDrop(name string) (int, error) {
	var tx DBTX
	if err := db.Begin(&tx); err != nil {
		return 0, err
	}
	deleted, err := tx.TableDrop(name)
	if err != nil {
		db.Abort(&tx)
		return 0, err
	}
	if err := db.Commit(&tx); err != nil {
		return 0, err
	}
	return deleted, nil
}

func checkRecord(def *TableDef, rec Record, required int) ([]Value, error) {
	if len(rec.Cols) != len(rec.Vals) || len(rec.Cols) != required {
		return nil, fmt.Errorf("record for %s requires %d columns", def.Name, required)
	}
	out := make([]Value, len(def.Cols))
	seen := map[string]bool{}
	for i, col := range rec.Cols {
		idx := slices.Index(def.Cols, col)
		if idx < 0 || idx >= required || seen[col] {
			return nil, fmt.Errorf("unexpected or duplicate column %q", col)
		}
		if rec.Vals[i].Type != def.Types[idx] {
			return nil, fmt.Errorf("type mismatch for column %q", col)
		}
		out[idx] = rec.Vals[i]
		seen[col] = true
	}
	for i := 0; i < required; i++ {
		if !seen[def.Cols[i]] {
			return nil, fmt.Errorf("missing column %q", def.Cols[i])
		}
	}
	return out, nil
}

func rowFromKV(def *TableDef, key, val []byte) (Record, error) {
	if len(key) < 4 {
		return Record{}, errors.New("truncated table key")
	}
	pk, err := decodeValues(key[4:], def.Types[:def.PKeys])
	if err != nil {
		return Record{}, err
	}
	rest, err := decodeValues(val, def.Types[def.PKeys:])
	if err != nil {
		return Record{}, err
	}
	vals := append(pk, rest...)
	return Record{Cols: append([]string(nil), def.Cols...), Vals: vals}, nil
}

func (tx *DBTX) get(def *TableDef, rec *Record) (bool, error) {
	vals, err := checkRecord(def, *rec, def.PKeys)
	if err != nil {
		return false, err
	}
	key := encodeKey(nil, def.Prefixes[0], vals[:def.PKeys])
	val, ok := tx.kv.Get(key)
	if !ok {
		return false, nil
	}
	row, err := rowFromKV(def, key, val)
	if err != nil {
		return false, err
	}
	*rec = row
	return true, nil
}

func (tx *DBTX) Get(table string, rec *Record) (bool, error) {
	def := tx.tables[table]
	if def == nil {
		return false, fmt.Errorf("table not found: %s", table)
	}
	return tx.get(def, rec)
}

func indexKeyColumns(def *TableDef, index int) []string {
	cols := append([]string(nil), def.Indexes[index]...)
	for _, pk := range def.Indexes[0] {
		if !slices.Contains(cols, pk) {
			cols = append(cols, pk)
		}
	}
	return cols
}

func valuesForColumns(def *TableDef, values []Value, cols []string) ([]Value, error) {
	out := make([]Value, len(cols))
	for i, col := range cols {
		idx := slices.Index(def.Cols, col)
		if idx < 0 || idx >= len(values) {
			return nil, fmt.Errorf("unknown column %q", col)
		}
		out[i] = values[idx]
	}
	return out, nil
}

func (tx *DBTX) set(def *TableDef, rec Record, mode int) (bool, error) {
	values, err := checkRecord(def, rec, len(def.Cols))
	if err != nil {
		return false, err
	}
	key := encodeKey(nil, def.Prefixes[0], values[:def.PKeys])
	val := encodeValues(nil, values[def.PKeys:])
	if len(key) > BTreeMaxKeySize {
		return false, fmt.Errorf("encoded primary key is %d bytes; maximum is %d", len(key), BTreeMaxKeySize)
	}
	if len(val) > BTreeMaxValSize {
		return false, fmt.Errorf("encoded row value is %d bytes; maximum is %d", len(val), BTreeMaxValSize)
	}
	indexKeys := make([][]byte, len(def.Indexes))
	for i := 1; i < len(def.Indexes); i++ {
		idxVals, _ := valuesForColumns(def, values, indexKeyColumns(def, i))
		indexKeys[i] = encodeKey(nil, def.Prefixes[i], idxVals)
		if len(indexKeys[i]) > BTreeMaxKeySize {
			return false, fmt.Errorf("encoded secondary-index key is %d bytes; maximum is %d", len(indexKeys[i]), BTreeMaxKeySize)
		}
	}
	req := UpdateReq{Key: key, Val: val, Mode: mode}
	if !tx.kv.Update(&req) {
		return false, nil
	}
	if !req.Added {
		old, err := rowFromKV(def, key, req.Old)
		if err != nil {
			return false, err
		}
		for i := 1; i < len(def.Indexes); i++ {
			oldVals, _ := valuesForColumns(def, old.Vals, indexKeyColumns(def, i))
			idxKey := encodeKey(nil, def.Prefixes[i], oldVals)
			tx.kv.Del(&DeleteReq{Key: idxKey})
		}
	}
	for i := 1; i < len(def.Indexes); i++ {
		tx.kv.Set(indexKeys[i], nil)
	}
	return true, nil
}

func (tx *DBTX) Set(table string, rec Record, mode int) (bool, error) {
	def := tx.tables[table]
	if def == nil {
		return false, fmt.Errorf("table not found: %s", table)
	}
	return tx.set(def, rec, mode)
}

func (tx *DBTX) Delete(table string, rec Record) (bool, error) {
	def := tx.tables[table]
	if def == nil {
		return false, fmt.Errorf("table not found: %s", table)
	}
	values, err := checkRecord(def, rec, def.PKeys)
	if err != nil {
		return false, err
	}
	key := encodeKey(nil, def.Prefixes[0], values[:def.PKeys])
	req := DeleteReq{Key: key}
	if !tx.kv.Del(&req) {
		return false, nil
	}
	old, err := rowFromKV(def, key, req.Old)
	if err != nil {
		return false, err
	}
	for i := 1; i < len(def.Indexes); i++ {
		idxVals, _ := valuesForColumns(def, old.Vals, indexKeyColumns(def, i))
		tx.kv.Del(&DeleteReq{Key: encodeKey(nil, def.Prefixes[i], idxVals)})
	}
	return true, nil
}

func (db *DB) Get(table string, rec *Record) (bool, error) {
	var tx DBTX
	if err := db.Begin(&tx); err != nil {
		return false, err
	}
	defer db.Abort(&tx)
	return tx.Get(table, rec)
}

func (db *DB) writeOne(table string, rec Record, mode int) (bool, error) {
	var tx DBTX
	if err := db.Begin(&tx); err != nil {
		return false, err
	}
	changed, err := tx.Set(table, rec, mode)
	if err != nil {
		db.Abort(&tx)
		return false, err
	}
	if err := db.Commit(&tx); err != nil {
		return false, err
	}
	return changed, nil
}

func (db *DB) Insert(table string, rec Record) (bool, error) {
	return db.writeOne(table, rec, MODE_INSERT_ONLY)
}
func (db *DB) Update(table string, rec Record) (bool, error) {
	return db.writeOne(table, rec, MODE_UPDATE_ONLY)
}
func (db *DB) Upsert(table string, rec Record) (bool, error) {
	return db.writeOne(table, rec, MODE_UPSERT)
}

func (db *DB) Delete(table string, rec Record) (bool, error) {
	var tx DBTX
	if err := db.Begin(&tx); err != nil {
		return false, err
	}
	changed, err := tx.Delete(table, rec)
	if err != nil {
		db.Abort(&tx)
		return false, err
	}
	if err := db.Commit(&tx); err != nil {
		return false, err
	}
	return changed, nil
}

// DeleteRangeResult distinguishes a complete bounded delete from one that has
// additional matching rows, allowing callers to page maintenance work.
type DeleteRangeResult struct {
	Deleted int
	More    bool
}

func (tx *DBTX) DeleteRange(table string, scan Scanner, limit int) (DeleteRangeResult, error) {
	if limit == 0 {
		limit = DefaultDeleteRangeRows
	}
	if limit < 1 || limit > MaxDeleteRangeRows {
		return DeleteRangeResult{}, fmt.Errorf("range delete row limit must be between 1 and %d", MaxDeleteRangeRows)
	}
	def := tx.tables[table]
	if def == nil {
		return DeleteRangeResult{}, fmt.Errorf("table not found: %s", table)
	}
	if err := tx.Scan(table, &scan); err != nil {
		return DeleteRangeResult{}, err
	}
	keys := make([]Record, 0, limit)
	for scan.Valid() && len(keys) < limit {
		var row Record
		if err := scan.Deref(&row); err != nil {
			return DeleteRangeResult{}, err
		}
		keys = append(keys, Record{Cols: append([]string(nil), def.Cols[:def.PKeys]...), Vals: append([]Value(nil), row.Vals[:def.PKeys]...)})
		scan.Next()
	}
	result := DeleteRangeResult{More: scan.Valid()}
	for _, key := range keys {
		changed, err := tx.Delete(table, key)
		if err != nil {
			return DeleteRangeResult{}, err
		}
		if changed {
			result.Deleted++
		}
	}
	return result, nil
}

func (db *DB) DeleteRange(table string, scan Scanner, limit int) (DeleteRangeResult, error) {
	var tx DBTX
	if err := db.Begin(&tx); err != nil {
		return DeleteRangeResult{}, err
	}
	result, err := tx.DeleteRange(table, scan, limit)
	if err != nil {
		db.Abort(&tx)
		return DeleteRangeResult{}, err
	}
	if err := db.Commit(&tx); err != nil {
		return DeleteRangeResult{}, err
	}
	return result, nil
}

// Mutation represents one element of an atomic, mixed-table write batch.
type Mutation struct {
	Table  string
	Record Record
	Mode   int
}

func (tx *DBTX) ApplyBatch(mutations []Mutation) (int, error) {
	if len(mutations) > MaxDeleteRangeRows {
		return 0, fmt.Errorf("write batch exceeds %d mutations", MaxDeleteRangeRows)
	}
	changed := 0
	for index, mutation := range mutations {
		var applied bool
		var err error
		if mutation.Mode == MODE_DELETE {
			applied, err = tx.Delete(mutation.Table, mutation.Record)
		} else {
			if mutation.Mode != MODE_UPSERT && mutation.Mode != MODE_UPDATE_ONLY && mutation.Mode != MODE_INSERT_ONLY {
				return 0, fmt.Errorf("mutation %d: invalid mode %d", index, mutation.Mode)
			}
			applied, err = tx.Set(mutation.Table, mutation.Record, mutation.Mode)
		}
		if err != nil {
			return 0, fmt.Errorf("mutation %d: %w", index, err)
		}
		if applied {
			changed++
		}
	}
	return changed, nil
}

func (db *DB) ApplyBatch(mutations []Mutation) (int, error) {
	var tx DBTX
	if err := db.Begin(&tx); err != nil {
		return 0, err
	}
	changed, err := tx.ApplyBatch(mutations)
	if err != nil {
		db.Abort(&tx)
		return 0, err
	}
	if err := db.Commit(&tx); err != nil {
		return 0, err
	}
	return changed, nil
}

type Scanner struct {
	Cmp1  int
	Cmp2  int
	Key1  Record
	Key2  Record
	Index []string // optional explicit index columns

	tx      *DBTX
	def     *TableDef
	index   int
	iter    *KVIterator
	endKey  []byte
	endCmp  int
	prefix  []byte
	lastErr error
}

func recordForIndexPrefix(def *TableDef, rec Record, index []string) ([]Value, error) {
	if len(rec.Cols) != len(rec.Vals) || len(rec.Cols) > len(index) {
		return nil, errors.New("scan key does not match index")
	}
	vals := make([]Value, len(rec.Cols))
	for i, col := range rec.Cols {
		if col != index[i] {
			return nil, fmt.Errorf("scan columns must be index prefix %v", index)
		}
		idx := slices.Index(def.Cols, col)
		if idx < 0 || rec.Vals[i].Type != def.Types[idx] {
			return nil, fmt.Errorf("invalid scan value for %q", col)
		}
		vals[i] = rec.Vals[i]
	}
	return vals, nil
}

func (tx *DBTX) Scan(table string, sc *Scanner) error {
	def := tx.tables[table]
	if def == nil {
		return fmt.Errorf("table not found: %s", table)
	}
	index := -1
	covered := func(cols, candidate []string) bool {
		return len(candidate) >= len(cols) && slices.Equal(candidate[:len(cols)], cols)
	}
	for i, candidate := range def.Indexes {
		if len(sc.Index) > 0 && !slices.Equal(sc.Index, candidate) {
			continue
		}
		if covered(sc.Key1.Cols, candidate) && covered(sc.Key2.Cols, candidate) {
			index = i
			break
		}
	}
	if index < 0 {
		return fmt.Errorf("no index covers scan keys %v and %v", sc.Key1.Cols, sc.Key2.Cols)
	}
	vals1, err := recordForIndexPrefix(def, sc.Key1, def.Indexes[index])
	if err != nil {
		return err
	}
	vals2, err := recordForIndexPrefix(def, sc.Key2, def.Indexes[index])
	if err != nil {
		return err
	}
	cmp1, cmp2 := sc.Cmp1, sc.Cmp2
	var start, end []byte
	if cmp1 == 0 && cmp2 == 0 {
		cmp1, cmp2 = CMP_GE, CMP_LT
		start = prefixKey(def.Prefixes[index])
		end = keySuccessor(start)
	} else if cmp1 != 0 && cmp2 == 0 {
		start = encodeKeyPartial(nil, def.Prefixes[index], vals1, cmp1)
		if cmp1 > 0 {
			cmp2 = CMP_LT
			end = keySuccessor(prefixKey(def.Prefixes[index]))
		} else {
			cmp2 = CMP_GE
			end = prefixKey(def.Prefixes[index])
		}
	} else if cmp1 == 0 && cmp2 != 0 {
		start = encodeKeyPartial(nil, def.Prefixes[index], vals2, cmp2)
		cmp1 = cmp2
		if cmp1 > 0 {
			cmp2 = CMP_LT
			end = keySuccessor(prefixKey(def.Prefixes[index]))
		} else {
			cmp2 = CMP_GE
			end = prefixKey(def.Prefixes[index])
		}
	} else {
		start = encodeKeyPartial(nil, def.Prefixes[index], vals1, cmp1)
		end = encodeKeyPartial(nil, def.Prefixes[index], vals2, cmp2)
	}
	sc.Cmp1, sc.Cmp2 = cmp1, cmp2
	sc.tx, sc.def, sc.index = tx, def, index
	sc.iter = tx.kv.seekNoTrack(start, cmp1)
	sc.endKey, sc.endCmp = end, cmp2
	sc.prefix = prefixKey(def.Prefixes[index])
	lo, hi := start, end
	if bytes.Compare(lo, hi) > 0 {
		lo, hi = hi, lo
	}
	tx.kv.TrackRange(lo, hi)
	return nil
}

func (sc *Scanner) Valid() bool {
	if sc == nil || sc.lastErr != nil || sc.iter == nil || !sc.iter.Valid() {
		return false
	}
	key, _ := sc.iter.Deref()
	if !bytes.HasPrefix(key, sc.prefix) {
		return false
	}
	cmp := bytes.Compare(key, sc.endKey)
	switch sc.endCmp {
	case CMP_LE:
		return cmp <= 0
	case CMP_LT:
		return cmp < 0
	case CMP_GE:
		return cmp >= 0
	case CMP_GT:
		return cmp > 0
	default:
		return false
	}
}

func (sc *Scanner) Next() {
	if sc.iter != nil {
		sc.iter.Next()
	}
}

func (sc *Scanner) Deref(rec *Record) error {
	if !sc.Valid() {
		return errors.New("dereference of invalid scanner")
	}
	key, val := sc.iter.Deref()
	if sc.index == 0 {
		row, err := rowFromKV(sc.def, key, val)
		if err != nil {
			return err
		}
		*rec = row
		return nil
	}
	cols := indexKeyColumns(sc.def, sc.index)
	types := make([]uint32, len(cols))
	for i, col := range cols {
		types[i] = sc.def.Types[slices.Index(sc.def.Cols, col)]
	}
	values, err := decodeValues(key[4:], types)
	if err != nil {
		return err
	}
	pk := Record{}
	for _, col := range sc.def.Indexes[0] {
		idx := slices.Index(cols, col)
		pk.Cols = append(pk.Cols, col)
		pk.Vals = append(pk.Vals, values[idx])
	}
	ok, err := sc.tx.get(sc.def, &pk)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("secondary index points to a missing row")
	}
	*rec = pk
	return nil
}
