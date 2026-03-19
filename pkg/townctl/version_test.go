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
	if !strings.Contains(stmt, "INSERT INTO desired_topology_versions") {
		t.Errorf("expected INSERT INTO desired_topology_versions, got: %s", stmt)
	}
	if !strings.Contains(stmt, "desired_cost_policy") {
		t.Errorf("expected table name 'desired_cost_policy' in statement, got: %s", stmt)
	}
	if !strings.Contains(stmt, "ON DUPLICATE KEY UPDATE") {
		t.Errorf("expected ON DUPLICATE KEY UPDATE (idempotent upsert), got: %s", stmt)
	}
}

func TestTopologyVersionsUpsert_MultipleTables(t *testing.T) {
	stmt := townctl.TopologyVersionsUpsert([]townctl.TableSchemaVersion{
		{Table: "desired_rigs", Version: 1},
		{Table: "desired_agent_config", Version: 1},
		{Table: "desired_formulas", Version: 1},
	})
	for _, name := range []string{"desired_rigs", "desired_agent_config", "desired_formulas"} {
		if !strings.Contains(stmt, name) {
			t.Errorf("expected table %q in statement, got: %s", name, stmt)
		}
	}
	// All three tables should be in a single INSERT statement.
	if count := strings.Count(stmt, "INSERT INTO"); count != 1 {
		t.Errorf("expected a single INSERT statement for multiple tables, got %d INSERT clauses: %s", count, stmt)
	}
}

func TestTopologyVersionsUpsert_UsesSchemaVersion(t *testing.T) {
	stmt := townctl.TopologyVersionsUpsert([]townctl.TableSchemaVersion{
		{Table: "desired_custom_roles", Version: 2},
	})
	if !strings.Contains(stmt, ", 2, ") {
		t.Errorf("expected schema version 2 in VALUES, got: %s", stmt)
	}
}

func TestTopologyVersionsUpsert_UsesBinaryVersion(t *testing.T) {
	original := townctl.BinaryVersion
	townctl.BinaryVersion = "town-ctl/1.2.3"
	defer func() { townctl.BinaryVersion = original }()

	stmt := townctl.TopologyVersionsUpsert([]townctl.TableSchemaVersion{
		{Table: "desired_rigs", Version: 1},
	})
	if !strings.Contains(stmt, "town-ctl/1.2.3") {
		t.Errorf("expected written_by to contain 'town-ctl/1.2.3', got: %s", stmt)
	}
}

func TestTopologyVersionsUpsert_EscapesSingleQuotes(t *testing.T) {
	stmt := townctl.TopologyVersionsUpsert([]townctl.TableSchemaVersion{
		{Table: "it's-a-table", Version: 1},
	})
	// Single quote in table name should be escaped as ''.
	if strings.Contains(stmt, "'s-a-table") && !strings.Contains(stmt, "''s-a-table") {
		t.Errorf("expected single quotes escaped in table name, got: %s", stmt)
	}
}
