// Package townctl implements the town-ctl actuator logic for applying Gas Town
// topology manifests to Dolt (ADR-0001, ADR-0006).
//
// This file implements the ADR-0003 contract (dgt-lx5): the
// desired_topology_versions write protocol that must be the first operation
// in every apply transaction.
package townctl

import (
	"fmt"
	"strings"
)

// BinaryVersion is the town-ctl version string written to desired_topology_versions
// as the written_by column. It defaults to "town-ctl/dev" and should be overridden
// at build time via:
//
//	go build -ldflags "-X github.com/tenev/dgt/pkg/townctl.BinaryVersion=town-ctl/0.1.0"
var BinaryVersion = "town-ctl/dev"

// TableSchemaVersion pairs a desired_topology table name with its current schema
// version, as defined in the corresponding SQL migration.
type TableSchemaVersion struct {
	// Table is the Dolt table name, e.g. "desired_cost_policy".
	Table string
	// Version is the integer schema version for this table at the time of writing.
	Version int
}

// TopologyVersionsUpsert returns a single Stmt that upserts one row into
// desired_topology_versions for each entry in tables. The statement uses
// BinaryVersion as the written_by value.
//
// Per ADR-0003 Decision 2, this statement MUST be the first SQL statement in
// every apply transaction that writes to any desired_topology table. The upsert
// is idempotent: re-applying with the same version is a no-op.
//
// Callers provide one TableSchemaVersion per topology table being written.
// Example:
//
//	stmts := []Stmt{
//	    TopologyVersionsUpsert([]TableSchemaVersion{
//	        {Table: "desired_rigs", Version: 1},
//	        {Table: "desired_agent_config", Version: 1},
//	    }),
//	    // ... table-specific upserts follow ...
//	}
func TopologyVersionsUpsert(tables []TableSchemaVersion) Stmt {
	values := make([]string, len(tables))
	args := make([]any, 0, len(tables)*3)
	for i, tv := range tables {
		values[i] = "(?, ?, ?)"
		args = append(args, tv.Table, tv.Version, BinaryVersion)
	}
	return Stmt{
		Query: fmt.Sprintf(
			"INSERT INTO desired_topology_versions (table_name, schema_version, written_by)"+
				" VALUES %s"+
				" ON DUPLICATE KEY UPDATE schema_version = VALUES(schema_version),"+
				" written_by = VALUES(written_by), written_at = CURRENT_TIMESTAMP;",
			strings.Join(values, ", ")),
		Args: args,
	}
}
