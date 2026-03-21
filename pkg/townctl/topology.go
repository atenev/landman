// Package townctl implements the town-ctl actuator logic for applying Gas Town
// topology manifests to Dolt (ADR-0001).
//
// This file implements the SQL generation for the core topology tables:
// desired_rigs, desired_agent_config, and desired_formulas (migration 001).
package townctl

import (
	"fmt"
	"os"
	"strings"

	"github.com/tenev/dgt/pkg/manifest"
)

const (
	desiredRigsSchemaVersion       = 1
	desiredAgentConfigSchemaVersion = 1
	desiredFormulasSchemaVersion    = 1
)

// TopologyApplySQL returns the ordered SQL statements that bring the core
// topology tables (desired_rigs, desired_agent_config, desired_formulas) in
// sync with m.
//
// Statement order (ADR-0003 contract):
//  1. desired_topology_versions upsert — MUST be first
//  2. UPSERT desired_rigs rows
//  3. DELETE removed rigs
//  4. UPSERT desired_agent_config rows
//  5. DELETE removed agent_config rows
//  6. UPSERT desired_formulas rows
//  7. DELETE removed formula rows
func TopologyApplySQL(m *manifest.TownManifest) []Stmt {
	stmts := make([]Stmt, 0, 10+len(m.Rigs)*5)

	// 1. ADR-0003: versions upsert first.
	stmts = append(stmts, TopologyVersionsUpsert([]TableSchemaVersion{
		{Table: "desired_rigs", Version: desiredRigsSchemaVersion},
		{Table: "desired_agent_config", Version: desiredAgentConfigSchemaVersion},
		{Table: "desired_formulas", Version: desiredFormulasSchemaVersion},
	}))

	// Collect rig names for cleanup DELETE statements.
	rigNames := make([]string, 0, len(m.Rigs))
	for _, rig := range m.Rigs {
		rigNames = append(rigNames, rig.Name)
	}

	// 2. UPSERT desired_rigs.
	for _, rig := range m.Rigs {
		stmts = append(stmts, upsertRigRow(rig))
	}

	// 3. DELETE removed rigs.
	stmts = append(stmts, deleteRemovedRigs(rigNames))

	// 4. UPSERT desired_agent_config — one row per (rig, enabled-role).
	// 5. DELETE removed agent_config rows — covered by cascading DELETE on rigs,
	//    but we also remove disabled-role rows explicitly.
	agentRows, agentKeys := resolveAgentConfigRows(m)
	for _, row := range agentRows {
		stmts = append(stmts, upsertAgentConfigRow(row))
	}
	stmts = append(stmts, deleteRemovedAgentConfig(agentKeys, rigNames))

	// 6. UPSERT desired_formulas.
	// 7. DELETE removed formula rows.
	formulaRows, formulaKeys := resolveFormulaRows(m)
	for _, row := range formulaRows {
		stmts = append(stmts, upsertFormulaRow(row))
	}
	stmts = append(stmts, deleteRemovedFormulas(formulaKeys, rigNames))

	return stmts
}

// agentConfigRow is one (rig_name, role) pair for desired_agent_config.
type agentConfigRow struct {
	RigName      string
	Role         string
	Enabled      bool
	Model        string
	MaxPolecats  int // only for role=polecat
	ClaudeMDPath string
}

// agentConfigKey uniquely identifies an agent_config row.
type agentConfigKey struct{ rigName, role string }

func resolveAgentConfigRows(m *manifest.TownManifest) ([]agentConfigRow, []agentConfigKey) {
	// Compute effective defaults for models and max_polecats.
	defaultMayorModel := m.Defaults.MayorModel
	if defaultMayorModel == "" {
		defaultMayorModel = "claude-opus-4-6"
	}
	defaultPolecatModel := m.Defaults.PolekatModel
	if defaultPolecatModel == "" {
		defaultPolecatModel = "claude-sonnet-4-6"
	}
	defaultMaxPolecats := m.Defaults.MaxPolecats
	if defaultMaxPolecats == 0 {
		defaultMaxPolecats = 20
	}

	rows := make([]agentConfigRow, 0, len(m.Rigs)*5)
	keys := make([]agentConfigKey, 0, len(m.Rigs)*5)

	for _, rig := range m.Rigs {
		mayorModel := ""
		if mayorModel == "" {
			mayorModel = defaultMayorModel
		}
		polecatModel := rig.Agents.PolekatModel
		if polecatModel == "" {
			polecatModel = defaultPolecatModel
		}
		maxPolecats := rig.Agents.MaxPolecats
		if maxPolecats == 0 {
			maxPolecats = defaultMaxPolecats
		}

		mayorClaudeMD := os.ExpandEnv(rig.Agents.MayorClaudeMD)

		type roleEntry struct {
			name    string
			enabled bool
			model   string
			maxPC   int
			md      string
		}
		entries := []roleEntry{
			{"mayor", rig.Agents.Mayor, mayorModel, 0, mayorClaudeMD},
			{"witness", rig.Agents.Witness, "", 0, ""},
			{"refinery", rig.Agents.Refinery, "", 0, ""},
			{"deacon", rig.Agents.Deacon, "", 0, ""},
			{"polecat", true, polecatModel, maxPolecats, ""},
		}

		for _, e := range entries {
			row := agentConfigRow{
				RigName:      rig.Name,
				Role:         e.name,
				Enabled:      e.enabled,
				Model:        e.model,
				MaxPolecats:  e.maxPC,
				ClaudeMDPath: e.md,
			}
			rows = append(rows, row)
			keys = append(keys, agentConfigKey{rig.Name, e.name})
		}
	}
	return rows, keys
}

func upsertRigRow(rig manifest.RigSpec) Stmt {
	enabled := 0
	if rig.Enabled {
		enabled = 1
	}
	return Stmt{
		Query: "INSERT INTO desired_rigs (name, repo, branch, enabled)" +
			" VALUES (?, ?, ?, ?)" +
			" ON DUPLICATE KEY UPDATE" +
			" repo = VALUES(repo), branch = VALUES(branch), enabled = VALUES(enabled);",
		Args: []any{rig.Name, rig.Repo, rig.Branch, enabled},
	}
}

func deleteRemovedRigs(rigNames []string) Stmt {
	if len(rigNames) == 0 {
		return Stmt{Query: "DELETE FROM desired_rigs;"}
	}
	placeholders := strings.Repeat("?, ", len(rigNames))
	placeholders = placeholders[:len(placeholders)-2]
	args := make([]any, len(rigNames))
	for i, n := range rigNames {
		args[i] = n
	}
	return Stmt{
		Query: fmt.Sprintf("DELETE FROM desired_rigs WHERE name NOT IN (%s);", placeholders),
		Args:  args,
	}
}

func upsertAgentConfigRow(r agentConfigRow) Stmt {
	enabled := 0
	if r.Enabled {
		enabled = 1
	}
	var model, claudeMD any
	if r.Model != "" {
		model = r.Model
	}
	if r.ClaudeMDPath != "" {
		claudeMD = r.ClaudeMDPath
	}
	var maxPolecats any
	if r.MaxPolecats > 0 {
		maxPolecats = r.MaxPolecats
	}
	return Stmt{
		Query: "INSERT INTO desired_agent_config (rig_name, role, enabled, model, max_polecats, claude_md_path)" +
			" VALUES (?, ?, ?, ?, ?, ?)" +
			" ON DUPLICATE KEY UPDATE" +
			" enabled = VALUES(enabled), model = VALUES(model)," +
			" max_polecats = VALUES(max_polecats), claude_md_path = VALUES(claude_md_path);",
		Args: []any{r.RigName, r.Role, enabled, model, maxPolecats, claudeMD},
	}
}

func deleteRemovedAgentConfig(keys []agentConfigKey, rigNames []string) Stmt {
	// Rows for deleted rigs are cascade-deleted. For remaining rigs, delete
	// any (rig, role) pairs not in the desired set.
	if len(keys) == 0 || len(rigNames) == 0 {
		return Stmt{Query: "DELETE FROM desired_agent_config WHERE 1=0;"} // no-op
	}
	rigPlaceholders := strings.Repeat("?, ", len(rigNames))
	rigPlaceholders = rigPlaceholders[:len(rigPlaceholders)-2]

	pairPlaceholders := strings.Repeat("(?, ?), ", len(keys))
	pairPlaceholders = pairPlaceholders[:len(pairPlaceholders)-2]

	args := make([]any, 0, len(rigNames)+len(keys)*2)
	for _, n := range rigNames {
		args = append(args, n)
	}
	for _, k := range keys {
		args = append(args, k.rigName, k.role)
	}
	return Stmt{
		Query: fmt.Sprintf(
			"DELETE FROM desired_agent_config"+
				" WHERE rig_name IN (%s) AND (rig_name, role) NOT IN (%s);",
			rigPlaceholders, pairPlaceholders),
		Args: args,
	}
}

// formulaRow is one (rig_name, formula_name) pair for desired_formulas.
type formulaRow struct {
	RigName  string
	Name     string
	Schedule string
}

type formulaKey struct{ rigName, name string }

func resolveFormulaRows(m *manifest.TownManifest) ([]formulaRow, []formulaKey) {
	rows := make([]formulaRow, 0, len(m.Rigs))
	keys := make([]formulaKey, 0, len(m.Rigs))
	for _, rig := range m.Rigs {
		for _, f := range rig.Formulas {
			rows = append(rows, formulaRow{
				RigName:  rig.Name,
				Name:     f.Name,
				Schedule: f.Schedule,
			})
			keys = append(keys, formulaKey{rig.Name, f.Name})
		}
	}
	return rows, keys
}

func upsertFormulaRow(r formulaRow) Stmt {
	return Stmt{
		Query: "INSERT INTO desired_formulas (rig_name, name, schedule)" +
			" VALUES (?, ?, ?)" +
			" ON DUPLICATE KEY UPDATE schedule = VALUES(schedule);",
		Args: []any{r.RigName, r.Name, r.Schedule},
	}
}

func deleteRemovedFormulas(keys []formulaKey, rigNames []string) Stmt {
	if len(rigNames) == 0 {
		return Stmt{Query: "DELETE FROM desired_formulas WHERE 1=0;"} // no-op
	}
	rigPlaceholders := strings.Repeat("?, ", len(rigNames))
	rigPlaceholders = rigPlaceholders[:len(rigPlaceholders)-2]
	rigArgs := make([]any, len(rigNames))
	for i, n := range rigNames {
		rigArgs[i] = n
	}
	if len(keys) == 0 {
		return Stmt{
			Query: fmt.Sprintf(
				"DELETE FROM desired_formulas WHERE rig_name IN (%s);",
				rigPlaceholders),
			Args: rigArgs,
		}
	}
	pairPlaceholders := strings.Repeat("(?, ?), ", len(keys))
	pairPlaceholders = pairPlaceholders[:len(pairPlaceholders)-2]

	args := make([]any, 0, len(rigNames)+len(keys)*2)
	args = append(args, rigArgs...)
	for _, k := range keys {
		args = append(args, k.rigName, k.name)
	}
	return Stmt{
		Query: fmt.Sprintf(
			"DELETE FROM desired_formulas"+
				" WHERE rig_name IN (%s) AND (rig_name, name) NOT IN (%s);",
			rigPlaceholders, pairPlaceholders),
		Args: args,
	}
}
