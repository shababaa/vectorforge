package byodb

import (
	"path/filepath"
	"testing"
)

func TestQueryLanguageEndToEnd(t *testing.T) {
	db, err := OpenDB(filepath.Join(t.TempDir(), "query.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	queries := []string{
		`CREATE TABLE users (
			id int,
			name string,
			age int,
			INDEX (age, name),
			PRIMARY KEY (id),
		);`,
		`INSERT INTO users (id, name, age) VALUES
			(1, 'Ada', 36), (2, 'Linus', 54), (3, 'Grace', 36), (4, 'Ken', 82);`,
	}
	for _, query := range queries {
		if _, err := db.Exec(query); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	result, err := db.Exec(`SELECT id, name, age + 1 AS next_age FROM users
		INDEX BY age = 36 FILTER name != 'Grace' LIMIT 10;`)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 1 || result.Rows[0].Get("name").String() != "Ada" || result.Rows[0].Get("next_age").I64 != 37 {
		t.Fatalf("unexpected SELECT result: %+v", result)
	}
	if _, err := db.Exec(`UPDATE users SET age = age + 1 INDEX BY age = 36 FILTER id = 3;`); err != nil {
		t.Fatal(err)
	}
	result, err = db.Exec(`SELECT * FROM users INDEX BY age >= 37 AND age < 60;`)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 2 {
		t.Fatalf("range SELECT returned %d rows, want 2", len(result.Rows))
	}
	result, err = db.Exec(`SELECT age FROM users INDEX BY age < 80 AND age >= 36;`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(result.Rows); i++ {
		if result.Rows[i-1].Get("age").I64 < result.Rows[i].Get("age").I64 {
			t.Fatalf("descending INDEX BY returned ascending ages: %+v", result.Rows)
		}
	}
	if result, err = db.Exec(`DELETE FROM users FILTER age > 70;`); err != nil || result.RowsAffected != 1 {
		t.Fatalf("DELETE=(%+v,%v)", result, err)
	}
}

func TestParserPrecedence(t *testing.T) {
	stmt, err := Parse(`SELECT 1 + 2 * 3 AS n FROM t FILTER NOT false AND 4 >= 3;`)
	if err != nil {
		t.Fatal(err)
	}
	selectStmt := stmt.(*QLSelect)
	value, err := eval(selectStmt.Output[0], Record{})
	if err != nil || value.I64 != 7 {
		t.Fatalf("expression=(%v,%v), want 7", value, err)
	}
}
