package townctl_test

import (
	"strings"
	"testing"

	"github.com/tenev/dgt/pkg/townctl"
)

func TestTopologyVersionsUpsert_SingleTable(t *testing.T) {
	stmt := townctl.TopologyVersionsUpsert([]townctl.TableSchemaVersion{
		{Table: "desired_cost_policy", Version: 1},
	})
	if !strings.Contains(stmt.Query, "INSERT INTO desired_topology_versions") {
		t.Errorf("expected INSERT INTO desired_topology_versions, got: %s", stmt.Query)
	}
	if !strings.Contains(stmt.Query, "desired_topology_versions") {
		t.Errorf("expected desired_topology_versions in query, got: %s", stmt.Query)
	}
	if !strings.Contains(stmt.Query, "ON DUPLICATE KEY UPDATE") {
		t.Errorf("expected ON DUPLICATE KEY UPDATE (idempotent upsert), got: %s", stmt.Query)
	}
	// Table name passed as arg, not embedded in query.
	if len(stmt.Args) < 1 || stmt.Args[0] != "desired_cost_policy" {
		t.Errorf("expected first arg to be 'desired_cost_policy', got args: %v", stmt.Args)
	}
}

func TestTopologyVersionsUpsert_MultipleTables(t *testing.T) {
	stmt := townctl.TopologyVersionsUpsert([]townctl.TableSchemaVersion{
		{Table: "desired_rigs", Version: 1},
		{Table: "desired_agent_config", Version: 1},
		{Table: "desired_formulas", Version: 1},
	})
	// All three tables should be in args (3 args per table: name, version, written_by).
	if len(stmt.Args) != 9 {
		t.Errorf("expected 9 args (3 tables x 3 cols), got %d: %v", len(stmt.Args), stmt.Args)
	}
	tableArgs := map[any]bool{}
	for _, a := range stmt.Args {
		tableArgs[a] = true
	}
	for _, name := range []string{"desired_rigs", "desired_agent_config", "desired_formulas"} {
		if !tableArgs[name] {
			t.Errorf("expected table %q in args, got: %v", name, stmt.Args)
		}
	}
	// All three rows should be in a single INSERT statement.
	if count := strings.Count(stmt.Query, "INSERT INTO"); count != 1 {
		t.Errorf("expected a single INSERT statement for multiple tables, got %d INSERT clauses: %s", count, stmt.Query)
	}
}

func TestTopologyVersionsUpsert_UsesSchemaVersion(t *testing.T) {
	stmt := townctl.TopologyVersionsUpsert([]townctl.TableSchemaVersion{
		{Table: "desired_custom_roles", Version: 2},
	})
	// Args are (table_name, schema_version, written_by) — schema_version is args[1].
	if len(stmt.Args) < 2 || stmt.Args[1] != 2 {
		t.Errorf("expected schema version 2 as second arg, got args: %v", stmt.Args)
	}
}

func TestTopologyVersionsUpsert_UsesBinaryVersion(t *testing.T) {
	original := townctl.BinaryVersion
	t.Cleanup(func() { townctl.BinaryVersion = original })
	townctl.BinaryVersion = "town-ctl/1.2.3"

	stmt := townctl.TopologyVersionsUpsert([]townctl.TableSchemaVersion{
		{Table: "desired_rigs", Version: 1},
	})
	// written_by is the third arg per table.
	if len(stmt.Args) < 3 || stmt.Args[2] != "town-ctl/1.2.3" {
		t.Errorf("expected written_by 'town-ctl/1.2.3' as third arg, got args: %v", stmt.Args)
	}
}

func TestTopologyVersionsUpsert_TableNamePassedAsArg(t *testing.T) {
	stmt := townctl.TopologyVersionsUpsert([]townctl.TableSchemaVersion{
		{Table: "it's-a-table", Version: 1},
	})
	// With parameterized queries, the table name is passed as an arg — no escaping needed.
	if len(stmt.Args) < 1 || stmt.Args[0] != "it's-a-table" {
		t.Errorf("expected table name passed as arg without escaping, got args: %v", stmt.Args)
	}
}
