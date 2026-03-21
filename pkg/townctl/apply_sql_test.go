package townctl

import (
	"testing"
)

// Tests for parseSQLActionTable (apply.go:278-298).

func TestParseSQLActionTable(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		wantAction string
		wantTable  string
	}{
		{
			name:       "too short returns empty",
			query:      "abc",
			wantAction: "",
			wantTable:  "",
		},
		{
			name:       "SET statement skipped",
			query:      "SET @dolt_transaction_commit_message = ?;",
			wantAction: "",
			wantTable:  "",
		},
		{
			name:       "INSERT INTO recognised",
			query:      "INSERT INTO desired_rigs (name) VALUES (?);",
			wantAction: "insert",
			wantTable:  "desired_rigs",
		},
		{
			name:       "DELETE FROM recognised",
			query:      "DELETE FROM desired_rigs WHERE name = ?;",
			wantAction: "delete",
			wantTable:  "desired_rigs",
		},
		{
			name:       "UPDATE recognised",
			query:      "UPDATE desired_rigs SET enabled = ? WHERE name = ?;",
			wantAction: "update",
			wantTable:  "desired_rigs",
		},
		{
			name:       "whitespace normalised across newline and tab",
			query:      "INSERT\t\nINTO  desired_rigs  (name) VALUES (?);",
			wantAction: "insert",
			wantTable:  "desired_rigs",
		},
		{
			name:       "backtick-quoted table name preserved",
			query:      "INSERT INTO `desired_rigs` (name) VALUES (?);",
			wantAction: "insert",
			wantTable:  "`desired_rigs`",
		},
		{
			name:       "UPDATE lowercase keyword recognised",
			query:      "update desired_rigs SET col = ?;",
			wantAction: "update",
			wantTable:  "desired_rigs",
		},
		{
			name:       "empty string returns empty",
			query:      "",
			wantAction: "",
			wantTable:  "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotAction, gotTable := parseSQLActionTable(tc.query)
			if gotAction != tc.wantAction {
				t.Errorf("action = %q, want %q", gotAction, tc.wantAction)
			}
			if gotTable != tc.wantTable {
				t.Errorf("table = %q, want %q", gotTable, tc.wantTable)
			}
		})
	}
}

// Tests for sqlFirstToken (apply.go:341-348).

func TestSQLFirstToken(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "empty string returns empty",
			input: "",
			want:  "",
		},
		{
			name:  "space delimiter",
			input: "desired_rigs WHERE name = ?",
			want:  "desired_rigs",
		},
		{
			name:  "paren delimiter (no preceding space)",
			input: "desired_rigs(col1, col2)",
			want:  "desired_rigs",
		},
		{
			name:  "tab delimiter",
			input: "desired_rigs\tcol",
			want:  "desired_rigs",
		},
		{
			name:  "newline delimiter",
			input: "desired_rigs\ncol",
			want:  "desired_rigs",
		},
		{
			name:  "no delimiter returns whole string",
			input: "desired_rigs",
			want:  "desired_rigs",
		},
		{
			name:  "token immediately before paren with no space",
			input: "INSERT(col)",
			want:  "INSERT",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sqlFirstToken(tc.input)
			if got != tc.want {
				t.Errorf("sqlFirstToken(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// Tests for recordDiffOps (apply.go:265-273).

// TestRecordDiffOps_NilAndEmpty verifies that nil and empty statement slices
// do not panic.
func TestRecordDiffOps_NilAndEmpty(t *testing.T) {
	recordDiffOps(nil)
	recordDiffOps([]Stmt{})
}

// TestRecordDiffOps_SkipsNonDML verifies that SET, COMMIT, and other non-DML
// statements are silently skipped without panicking.
func TestRecordDiffOps_SkipsNonDML(t *testing.T) {
	stmts := []Stmt{
		{Query: "SET @dolt_transaction_commit_message = ?;", Args: []any{"msg"}},
		{Query: "COMMIT;"},
		{Query: ""},
	}
	recordDiffOps(stmts)
}

// TestRecordDiffOps_DMLStatements verifies that INSERT, DELETE, and UPDATE
// statements are processed without panicking (metric increments are side effects).
func TestRecordDiffOps_DMLStatements(t *testing.T) {
	stmts := []Stmt{
		{Query: "INSERT INTO desired_rigs (name) VALUES (?);", Args: []any{"r1"}},
		{Query: "DELETE FROM desired_rigs WHERE name = ?;", Args: []any{"old"}},
		{Query: "UPDATE desired_rigs SET enabled = ? WHERE name = ?;", Args: []any{true, "r2"}},
	}
	recordDiffOps(stmts)
}
