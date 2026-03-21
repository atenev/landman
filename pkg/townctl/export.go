// Package townctl implements the town-ctl actuator logic for applying Gas Town
// topology manifests to Dolt (ADR-0001, ADR-0006).
//
// This file implements town-ctl export (dgt-6u7): read-only reconstruction of
// a town.toml skeleton or K8s CRD manifests from the current desired_topology
// Dolt tables. Export is strictly read-only — it never writes to Dolt.
//
// Two backends are supported:
//   - local: emits a town.toml skeleton with placeholder town-level fields that
//     cannot be derived from Dolt (name, home, secrets).
//   - k8s:   emits YAML for GasTown, Rig, and AgentRole CRDs (ADR-0005).
package townctl

import (
	"fmt"
	"strings"
)

// ExportBackend selects the output format for Export.
type ExportBackend string

const (
	// BackendLocal emits a town.toml skeleton.
	BackendLocal ExportBackend = "local"
	// BackendK8s emits Kubernetes CRD manifest YAML.
	BackendK8s ExportBackend = "k8s"
)

// ExportOptions configures the Export call.
type ExportOptions struct {
	// Backend selects the output format. Required.
	Backend ExportBackend
	// Namespace is the Kubernetes namespace for namespaced CRs (k8s backend only).
	// Defaults to "gas-town" when empty.
	Namespace string
	// GasTownName is the Kubernetes CR name for the GasTown resource (k8s backend only).
	// Defaults to "gas-town" when empty.
	GasTownName string
}

// DesiredRigRow is one row from desired_rigs.
type DesiredRigRow struct {
	Name    string
	Repo    string
	Branch  string
	Enabled bool
}

// DesiredAgentConfigRow is one row from desired_agent_config.
// Role is one of: mayor, witness, refinery, deacon, polecat.
type DesiredAgentConfigRow struct {
	RigName      string
	Role         string
	Enabled      bool
	Model        string
	MaxPolecats  int // non-zero for polecat rows only
	ClaudeMDPath string
}

// DesiredFormulaRow is one row from desired_formulas.
type DesiredFormulaRow struct {
	RigName  string
	Name     string
	Schedule string
}

// DesiredCustomRoleRow is one row from desired_custom_roles.
type DesiredCustomRoleRow struct {
	Name            string
	Description     string
	Scope           string // "rig" or "town"
	Lifespan        string // "ephemeral" or "persistent"
	TriggerType     string // "bead_assigned", "schedule", "event", "manual"
	TriggerSchedule string // non-empty when TriggerType == "schedule"
	TriggerEvent    string // non-empty when TriggerType == "event"
	ParentRole      string
	ReportsTo       string
	MaxInstances    int
	ClaudeMDPath    string
	Model           string
}

// DesiredRigCustomRoleRow is one row from desired_rig_custom_roles.
type DesiredRigCustomRoleRow struct {
	RigName  string
	RoleName string
	Enabled  bool
}

// ExportState holds all rows read from Dolt desired_topology tables.
// It is a pure value type — callers are responsible for populating it from Dolt.
// Export never connects to Dolt itself.
type ExportState struct {
	Rigs           []DesiredRigRow
	AgentConfig    []DesiredAgentConfigRow
	Formulas       []DesiredFormulaRow
	CustomRoles    []DesiredCustomRoleRow
	RigCustomRoles []DesiredRigCustomRoleRow
	CostPolicies   []CostPolicyRow // reused from costpolicy.go
}

// Export generates a town.toml skeleton or K8s CRD manifests from state.
// It is read-only with respect to Dolt — it never connects to or modifies Dolt.
func Export(state ExportState, opts ExportOptions) (string, error) {
	switch opts.Backend {
	case BackendLocal:
		return generateTOML(state), nil
	case BackendK8s:
		ns := opts.Namespace
		if ns == "" {
			ns = "gas-town"
		}
		gtName := opts.GasTownName
		if gtName == "" {
			gtName = "gas-town"
		}
		return generateCRDs(state, ns, gtName), nil
	default:
		return "", fmt.Errorf("unknown export backend %q: must be %q or %q",
			opts.Backend, BackendLocal, BackendK8s)
	}
}

// ─── TOML backend ─────────────────────────────────────────────────────────────

func generateTOML(state ExportState) string {
	var b strings.Builder

	b.WriteString("# town.toml — generated skeleton (town-ctl export --backend=local)\n")
	b.WriteString("#\n")
	b.WriteString("# Fields marked FIXME cannot be derived from Dolt and must be filled in.\n")
	b.WriteString("# All other values are reconstructed from desired_topology.\n\n")

	b.WriteString("version = \"1\"\n\n")

	b.WriteString("[town]\n")
	b.WriteString("name      = \"FIXME\"   # not stored in Dolt — fill in your town name\n")
	b.WriteString("home      = \"FIXME\"   # not stored in Dolt — fill in your GT_HOME path\n")
	b.WriteString("dolt_port = 3306\n\n")

	b.WriteString("[town.agents]\n")
	b.WriteString("surveyor = false   # set to true if you run the Surveyor\n\n")

	b.WriteString("[secrets]\n")
	b.WriteString("anthropic_api_key = \"${ANTHROPIC_API_KEY}\"\n")
	b.WriteString("github_token      = \"${GITHUB_TOKEN}\"\n\n")

	writeDefaultsSection(&b, state)
	writeRoleSections(&b, state)
	writeRigSections(&b, state)

	return b.String()
}

func writeDefaultsSection(b *strings.Builder, state ExportState) {
	b.WriteString("[defaults]\n")

	// Derive defaults from the most common agent config values across all rigs.
	defaultModel := ""
	defaultPolekatModel := ""
	defaultMaxPolecats := 0
	for _, row := range state.AgentConfig {
		if row.Role == "mayor" && row.Model != "" && defaultModel == "" {
			defaultModel = row.Model
		}
		if row.Role == "polecat" {
			if row.Model != "" && defaultPolekatModel == "" {
				defaultPolekatModel = row.Model
			}
			if row.MaxPolecats > defaultMaxPolecats {
				defaultMaxPolecats = row.MaxPolecats
			}
		}
	}
	if defaultModel == "" {
		defaultModel = "claude-sonnet-4-6"
	}
	if defaultPolekatModel == "" {
		defaultPolekatModel = "claude-haiku-4-5-20251001"
	}
	if defaultMaxPolecats == 0 {
		defaultMaxPolecats = 5
	}

	fmt.Fprintf(b, "mayor_model   = %q\n", defaultModel)
	fmt.Fprintf(b, "polecat_model = %q\n", defaultPolekatModel)
	fmt.Fprintf(b, "max_polecats  = %d\n", defaultMaxPolecats)

	// Emit a cost block if all rigs share the same cost policy (common default).
	commonCost := sharedCostPolicy(state.CostPolicies, state.Rigs)
	if commonCost != nil {
		b.WriteString("\n[defaults.cost]\n")
		writeCostPolicyFields(b, *commonCost)
	}

	b.WriteString("\n")
}

// sharedCostPolicy returns the CostPolicyRow if all rigs share the same budget
// type/budget/warn_at_pct values, and nil otherwise. Used to hoist common cost
// policy to [defaults.cost] rather than repeating it per rig.
func sharedCostPolicy(policies []CostPolicyRow, rigs []DesiredRigRow) *CostPolicyRow {
	if len(policies) == 0 || len(rigs) != len(policies) {
		return nil
	}
	first := policies[0]
	for _, p := range policies[1:] {
		if p.BudgetType != first.BudgetType ||
			p.DailyBudget != first.DailyBudget ||
			p.WarnAtPct != first.WarnAtPct {
			return nil
		}
	}
	return &first
}

func writeCostPolicyFields(b *strings.Builder, row CostPolicyRow) {
	switch row.BudgetType {
	case "usd":
		fmt.Fprintf(b, "daily_budget_usd = %.4f\n", row.DailyBudget)
	case "messages":
		fmt.Fprintf(b, "daily_budget_messages = %d\n", int64(row.DailyBudget))
	case "tokens":
		fmt.Fprintf(b, "daily_budget_tokens = %d\n", int64(row.DailyBudget))
	}
	fmt.Fprintf(b, "warn_at_pct = %d\n", row.WarnAtPct)
}

func writeRoleSections(b *strings.Builder, state ExportState) {
	if len(state.CustomRoles) == 0 {
		return
	}
	for _, role := range state.CustomRoles {
		b.WriteString("[[role]]\n")
		fmt.Fprintf(b, "name  = %q\n", role.Name)
		if role.Description != "" {
			fmt.Fprintf(b, "description = %q\n", role.Description)
		}
		fmt.Fprintf(b, "scope    = %q\n", role.Scope)
		if role.Lifespan != "" {
			fmt.Fprintf(b, "lifespan = %q\n", role.Lifespan)
		}
		b.WriteString("\n[role.identity]\n")
		fmt.Fprintf(b, "claude_md = %q\n", role.ClaudeMDPath)
		if role.Model != "" {
			fmt.Fprintf(b, "model     = %q\n", role.Model)
		}
		b.WriteString("\n[role.trigger]\n")
		fmt.Fprintf(b, "type = %q\n", role.TriggerType)
		if role.TriggerSchedule != "" {
			fmt.Fprintf(b, "schedule = %q\n", role.TriggerSchedule)
		}
		if role.TriggerEvent != "" {
			fmt.Fprintf(b, "event = %q\n", role.TriggerEvent)
		}
		b.WriteString("\n[role.supervision]\n")
		fmt.Fprintf(b, "parent = %q\n", role.ParentRole)
		if role.ReportsTo != "" {
			fmt.Fprintf(b, "reports_to = %q\n", role.ReportsTo)
		}
		if role.MaxInstances > 0 {
			b.WriteString("\n[role.resources]\n")
			fmt.Fprintf(b, "max_instances = %d\n", role.MaxInstances)
		}
		b.WriteString("\n")
	}
}

func writeRigSections(b *strings.Builder, state ExportState) {
	// Index agent config, formulas, custom roles, and cost policies by rig name.
	agentByRig := make(map[string][]DesiredAgentConfigRow)
	for _, row := range state.AgentConfig {
		agentByRig[row.RigName] = append(agentByRig[row.RigName], row)
	}
	formulasByRig := make(map[string][]DesiredFormulaRow)
	for _, row := range state.Formulas {
		formulasByRig[row.RigName] = append(formulasByRig[row.RigName], row)
	}
	rolesByRig := make(map[string][]string)
	for _, row := range state.RigCustomRoles {
		if row.Enabled {
			rolesByRig[row.RigName] = append(rolesByRig[row.RigName], row.RoleName)
		}
	}
	costByRig := make(map[string]CostPolicyRow)
	for _, row := range state.CostPolicies {
		costByRig[row.RigName] = row
	}

	sharedCost := sharedCostPolicy(state.CostPolicies, state.Rigs)

	for _, rig := range state.Rigs {
		b.WriteString("[[rig]]\n")
		fmt.Fprintf(b, "name    = %q\n", rig.Name)
		fmt.Fprintf(b, "repo    = %q\n", rig.Repo)
		fmt.Fprintf(b, "branch  = %q\n", rig.Branch)
		fmt.Fprintf(b, "enabled = %v\n", rig.Enabled)

		writeAgentConfigSection(b, agentByRig[rig.Name], rolesByRig[rig.Name])
		writeFormulaSections(b, formulasByRig[rig.Name])

		if cost, ok := costByRig[rig.Name]; ok && sharedCost == nil {
			b.WriteString("\n[rig.cost]\n")
			writeCostPolicyFields(b, cost)
		}

		b.WriteString("\n")
	}
}

func writeAgentConfigSection(b *strings.Builder, rows []DesiredAgentConfigRow, customRoles []string) {
	if len(rows) == 0 && len(customRoles) == 0 {
		return
	}
	b.WriteString("\n[rig.agents]\n")

	agentByRole := make(map[string]DesiredAgentConfigRow)
	for _, row := range rows {
		agentByRole[row.Role] = row
	}

	for _, role := range []string{"mayor", "witness", "refinery", "deacon"} {
		if row, ok := agentByRole[role]; ok {
			fmt.Fprintf(b, "%-8s = %v\n", role, row.Enabled)
			if row.Model != "" && role == "mayor" {
				fmt.Fprintf(b, "mayor_model     = %q\n", row.Model)
			}
			if row.ClaudeMDPath != "" && role == "mayor" {
				fmt.Fprintf(b, "mayor_claude_md = %q\n", row.ClaudeMDPath)
			}
		}
	}
	if row, ok := agentByRole["polecat"]; ok {
		if row.MaxPolecats > 0 {
			fmt.Fprintf(b, "max_polecats = %d\n", row.MaxPolecats)
		}
		if row.Model != "" {
			fmt.Fprintf(b, "polecat_model = %q\n", row.Model)
		}
	}
	if len(customRoles) > 0 {
		quoted := make([]string, len(customRoles))
		for i, r := range customRoles {
			quoted[i] = fmt.Sprintf("%q", r)
		}
		fmt.Fprintf(b, "roles = [%s]\n", strings.Join(quoted, ", "))
	}
}

func writeFormulaSections(b *strings.Builder, rows []DesiredFormulaRow) {
	for _, row := range rows {
		b.WriteString("\n[[rig.formula]]\n")
		fmt.Fprintf(b, "name     = %q\n", row.Name)
		fmt.Fprintf(b, "schedule = %q\n", row.Schedule)
	}
}

// ─── K8s CRD backend ──────────────────────────────────────────────────────────

func generateCRDs(state ExportState, namespace, gasTownName string) string {
	var b strings.Builder

	b.WriteString("# Kubernetes CRD manifests — generated (town-ctl export --backend=k8s)\n")
	b.WriteString("#\n")
	b.WriteString("# Fields marked FIXME cannot be derived from Dolt and must be filled in.\n")
	b.WriteString("# Apply with: kubectl apply -f this-file.yaml\n\n")

	writeGasTownCR(&b, state, namespace, gasTownName)

	for _, rig := range state.Rigs {
		b.WriteString("---\n")
		writeRigCR(&b, rig, state, namespace, gasTownName)
	}

	for _, role := range state.CustomRoles {
		b.WriteString("---\n")
		writeAgentRoleCR(&b, role, namespace, gasTownName)
	}

	return b.String()
}

func writeGasTownCR(b *strings.Builder, state ExportState, namespace, name string) {
	b.WriteString("apiVersion: gastown.tenev.io/v1alpha1\n")
	b.WriteString("kind: GasTown\n")
	b.WriteString("metadata:\n")
	fmt.Fprintf(b, "  name: %s\n", name)
	b.WriteString("spec:\n")
	b.WriteString("  version: \"1\"\n")
	b.WriteString("  home: FIXME   # not stored in Dolt\n")
	fmt.Fprintf(b, "  doltRef:\n    name: dolt\n    namespace: %s\n", namespace)

	// Emit defaults from the common agent config.
	defaultMayorModel := ""
	defaultPolekatModel := ""
	defaultMaxPolecats := 0
	for _, row := range state.AgentConfig {
		if row.Role == "mayor" && row.Model != "" && defaultMayorModel == "" {
			defaultMayorModel = row.Model
		}
		if row.Role == "polecat" {
			if row.Model != "" && defaultPolekatModel == "" {
				defaultPolekatModel = row.Model
			}
			if row.MaxPolecats > defaultMaxPolecats {
				defaultMaxPolecats = row.MaxPolecats
			}
		}
	}
	if defaultMayorModel != "" || defaultPolekatModel != "" || defaultMaxPolecats > 0 {
		b.WriteString("  defaults:\n")
		if defaultMayorModel != "" {
			fmt.Fprintf(b, "    mayorModel: %q\n", defaultMayorModel)
		}
		if defaultPolekatModel != "" {
			fmt.Fprintf(b, "    polecatModel: %q\n", defaultPolekatModel)
		}
		if defaultMaxPolecats > 0 {
			fmt.Fprintf(b, "    maxPolecats: %d\n", defaultMaxPolecats)
		}
	}

	// Cost defaults.
	sharedCost := sharedCostPolicy(state.CostPolicies, state.Rigs)
	if sharedCost != nil {
		b.WriteString("  cost:\n")
		writeCostPolicyFieldsYAML(b, *sharedCost, "    ")
	}

	b.WriteString("  secretsRef:\n")
	b.WriteString("    name: FIXME   # name of a Kubernetes Secret with API keys\n")
	b.WriteString("\n")
}

func writeRigCR(b *strings.Builder, rig DesiredRigRow, state ExportState, namespace, gasTownRef string) {
	b.WriteString("apiVersion: gastown.tenev.io/v1alpha1\n")
	b.WriteString("kind: Rig\n")
	b.WriteString("metadata:\n")
	fmt.Fprintf(b, "  name: %s\n", rig.Name)
	fmt.Fprintf(b, "  namespace: %s\n", namespace)
	b.WriteString("spec:\n")
	fmt.Fprintf(b, "  gasTownRef:\n    name: %s\n", gasTownRef)
	fmt.Fprintf(b, "  repo: %q\n", rig.Repo)
	fmt.Fprintf(b, "  branch: %q\n", rig.Branch)
	fmt.Fprintf(b, "  enabled: %v\n", rig.Enabled)

	agentByRole := make(map[string]DesiredAgentConfigRow)
	for _, row := range state.AgentConfig {
		if row.RigName == rig.Name {
			agentByRole[row.Role] = row
		}
	}

	writtenAgents := false
	for _, role := range []string{"mayor", "witness", "refinery", "deacon"} {
		if row, ok := agentByRole[role]; ok {
			if !writtenAgents {
				b.WriteString("  agents:\n")
				writtenAgents = true
			}
			fmt.Fprintf(b, "    %s: %v\n", role, row.Enabled)
			if role == "mayor" && row.Model != "" {
				fmt.Fprintf(b, "    mayorModel: %q\n", row.Model)
			}
		}
	}
	if row, ok := agentByRole["polecat"]; ok {
		if !writtenAgents {
			b.WriteString("  agents:\n")
			writtenAgents = true
		}
		if row.MaxPolecats > 0 {
			fmt.Fprintf(b, "    maxPolecats: %d\n", row.MaxPolecats)
		}
	}
	_ = writtenAgents

	// Formula refs.
	for _, formula := range state.Formulas {
		if formula.RigName == rig.Name {
			b.WriteString("  formulas:\n")
			break
		}
	}
	for _, formula := range state.Formulas {
		if formula.RigName != rig.Name {
			continue
		}
		fmt.Fprintf(b, "  - name: %q\n    schedule: %q\n", formula.Name, formula.Schedule)
	}

	// Per-rig custom role refs.
	for _, rcr := range state.RigCustomRoles {
		if rcr.RigName == rig.Name && rcr.Enabled {
			b.WriteString("  roles:\n")
			break
		}
	}
	for _, rcr := range state.RigCustomRoles {
		if rcr.RigName != rig.Name || !rcr.Enabled {
			continue
		}
		fmt.Fprintf(b, "  - name: %q\n", rcr.RoleName)
	}

	// Per-rig cost policy (only if not already emitted at defaults level).
	sharedCost := sharedCostPolicy(state.CostPolicies, state.Rigs)
	if sharedCost == nil {
		for _, cp := range state.CostPolicies {
			if cp.RigName == rig.Name {
				b.WriteString("  cost:\n")
				writeCostPolicyFieldsYAML(b, cp, "    ")
				break
			}
		}
	}

	b.WriteString("\n")
}

func writeAgentRoleCR(b *strings.Builder, role DesiredCustomRoleRow, namespace, gasTownRef string) {
	b.WriteString("apiVersion: gastown.tenev.io/v1alpha1\n")
	b.WriteString("kind: AgentRole\n")
	b.WriteString("metadata:\n")
	fmt.Fprintf(b, "  name: %s\n", role.Name)
	fmt.Fprintf(b, "  namespace: %s\n", namespace)
	b.WriteString("spec:\n")
	fmt.Fprintf(b, "  gasTownRef:\n    name: %s\n", gasTownRef)
	if role.Description != "" {
		fmt.Fprintf(b, "  description: %q\n", role.Description)
	}
	fmt.Fprintf(b, "  scope: %q\n", role.Scope)
	if role.Lifespan != "" {
		fmt.Fprintf(b, "  lifespan: %q\n", role.Lifespan)
	}
	b.WriteString("  identity:\n")
	fmt.Fprintf(b, "    claudeMD: %q\n", role.ClaudeMDPath)
	if role.Model != "" {
		fmt.Fprintf(b, "    model: %q\n", role.Model)
	}
	b.WriteString("  trigger:\n")
	fmt.Fprintf(b, "    type: %q\n", role.TriggerType)
	if role.TriggerSchedule != "" {
		fmt.Fprintf(b, "    schedule: %q\n", role.TriggerSchedule)
	}
	if role.TriggerEvent != "" {
		fmt.Fprintf(b, "    event: %q\n", role.TriggerEvent)
	}
	b.WriteString("  supervision:\n")
	fmt.Fprintf(b, "    parent: %q\n", role.ParentRole)
	if role.ReportsTo != "" {
		fmt.Fprintf(b, "    reportsTo: %q\n", role.ReportsTo)
	}
	if role.MaxInstances > 0 {
		b.WriteString("  resources:\n")
		fmt.Fprintf(b, "    maxInstances: %d\n", role.MaxInstances)
	}
	b.WriteString("\n")
}

func writeCostPolicyFieldsYAML(b *strings.Builder, row CostPolicyRow, indent string) {
	switch row.BudgetType {
	case "usd":
		fmt.Fprintf(b, "%sdailyBudgetUSD: %.4f\n", indent, row.DailyBudget)
	case "messages":
		fmt.Fprintf(b, "%sdailyBudgetMessages: %d\n", indent, int64(row.DailyBudget))
	case "tokens":
		fmt.Fprintf(b, "%sdailyBudgetTokens: %d\n", indent, int64(row.DailyBudget))
	}
	fmt.Fprintf(b, "%swarnAtPct: %d\n", indent, row.WarnAtPct)
}

// ExportQuerySQL returns the ordered SQL SELECT statements that callers should
// run against Dolt (in order) to populate an ExportState for Export.
// Statements return columns in the order matching each Desired*Row type.
//
// Callers execute these queries, scan results into the corresponding slice
// types, and assemble an ExportState before calling Export.
func ExportQuerySQL() []string {
	return []string{
		"SELECT name, repo, branch, enabled FROM desired_rigs ORDER BY name;",
		"SELECT rig_name, role, enabled, model, max_polecats, claude_md_path FROM desired_agent_config ORDER BY rig_name, role;",
		"SELECT rig_name, name, schedule FROM desired_formulas ORDER BY rig_name, name;",
		"SELECT name, description, scope, lifespan, trigger_type, trigger_schedule, trigger_event, parent_role, reports_to_role, max_instances, claude_md_path, model FROM desired_custom_roles ORDER BY name;",
		"SELECT rig_name, role_name, enabled FROM desired_rig_custom_roles ORDER BY rig_name, role_name;",
		"SELECT rig_name, budget_type, daily_budget, warn_at_pct FROM desired_cost_policy ORDER BY rig_name;",
	}
}
