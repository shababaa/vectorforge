package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"

	"byodb"
)

func main() {
	path := flag.String("db", "byodb.db", "database file")
	command := flag.String("c", "", "execute one query and exit")
	readOnly := flag.Bool("read-only", false, "open an existing database without write access")
	backup := flag.String("backup", "", "write a durable point-in-time copy and exit")
	flag.Parse()

	db, err := byodb.OpenDBWithOptions(*path, byodb.DBOptions{ReadOnly: *readOnly})
	if err != nil {
		fatal(err)
	}
	defer db.Close()
	if *backup != "" {
		if err := db.Backup(*backup); err != nil {
			fatal(err)
		}
		fmt.Printf("backup created: %s\n", *backup)
		return
	}
	if *command != "" {
		result, err := db.Exec(*command)
		if err != nil {
			fatal(err)
		}
		printResult(result)
		return
	}

	fmt.Printf("byodb - %s\nType .help for help; terminate statements with ';'.\n", *path)
	scanner := bufio.NewScanner(os.Stdin)
	var query strings.Builder
	for {
		if query.Len() == 0 {
			fmt.Print("byodb> ")
		} else {
			fmt.Print("   ...> ")
		}
		if !scanner.Scan() {
			fmt.Println()
			return
		}
		line := strings.TrimSpace(scanner.Text())
		if query.Len() == 0 && strings.HasPrefix(line, ".") {
			switch line {
			case ".quit", ".exit":
				return
			case ".help":
				fmt.Println(".tables  list tables\n.quit    exit\nSQL-like statements: CREATE, DROP, INSERT, SELECT, UPDATE, DELETE")
			case ".tables":
				result, err := db.Exec("SELECT name FROM @table;")
				if err != nil {
					fmt.Fprintln(os.Stderr, "error:", err)
				} else {
					printResult(result)
				}
			default:
				fmt.Fprintln(os.Stderr, "unknown command")
			}
			continue
		}
		query.WriteString(line)
		query.WriteByte('\n')
		if !strings.HasSuffix(line, ";") {
			continue
		}
		result, err := db.Exec(query.String())
		query.Reset()
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			continue
		}
		printResult(result)
	}
}

func printResult(result *byodb.Result) {
	if len(result.Columns) > 0 {
		fmt.Println(strings.Join(result.Columns, "\t"))
		for _, row := range result.Rows {
			items := make([]string, len(row.Vals))
			for i, value := range row.Vals {
				items[i] = value.String()
			}
			fmt.Println(strings.Join(items, "\t"))
		}
		fmt.Printf("%d row(s)\n", len(result.Rows))
	} else if result.Message != "" {
		fmt.Println(result.Message)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "byodb:", err)
	os.Exit(1)
}
