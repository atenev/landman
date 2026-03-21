// Package townctl implements the town-ctl actuator logic for applying Gas Town
// topology manifests to Dolt (ADR-0001, ADR-0006).
//
// This file implements text and JSON rendering of StatusResult (ADR-0012).
package townctl

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// FormatOpts controls rendering of StatusResult.
type FormatOpts struct {
	// NoColor disables ANSI colour codes. Auto-detected from a TTY check in the
	// caller; also set when --no-color flag is passed.
	NoColor bool
}

// ANSI colour codes.
const (
	ansiReset  = "\033[0m"
	ansiRed    = "\033[31m"
	ansiYellow = "\033[33m"
)

// Column widths as constants per spec (no dynamic query for layout).
const (
	colRig      = 12
	colStatus   = 10
	colMayor    = 9
	colPolecats = 10
	colScore    = 7
	colStale    = 8
)

// FormatStatusText renders r as an ANSI-coloured terminal table.
// Colour is suppressed when opts.NoColor is true.
func FormatStatusText(r *StatusResult, opts FormatOpts) string {
	var b strings.Builder

	color := func(code, s string) string {
		if opts.NoColor {
			return s
		}
		return code + s + ansiReset
	}

	// Fleet status icon.
	icon := "✓"
	for _, rig := range r.Rigs {
		if rig.Score == 0.0 {
			icon = "✗"
			break
		} else if rig.Score < 1.0 || rig.MayorStaleSeconds > 0 {
			icon = "⚠"
		}
	}

	// Header line.
	age := ""
	// ReadAt is when we read data; use it as a reference.
	elapsed := time.Since(r.ReadAt)
	if elapsed > time.Second {
		age = fmt.Sprintf(" (%s ago)", formatDuration(elapsed))
	}
	b.WriteString(fmt.Sprintf("TOWN: %-12s score=%.2f   %s%s\n\n",
		truncate(r.Town, 12), r.Score, icon, age))

	// Rig table header.
	b.WriteString(fmt.Sprintf("%-*s %-*s %-*s %-*s %-*s %-*s\n",
		colRig, "RIG",
		colStatus, "STATUS",
		colMayor, "MAYOR",
		colPolecats, "POLECATS",
		colScore, "SCORE",
		colStale, "STALE",
	))

	for _, rig := range r.Rigs {
		// Score colour.
		scoreStr := fmt.Sprintf("%.2f", rig.Score)
		switch {
		case rig.Score == 0.0:
			scoreStr = color(ansiRed, scoreStr)
		case rig.Score < 0.9:
			scoreStr = color(ansiYellow, scoreStr)
		}

		// Stale column.
		staleStr := "—"
		if rig.MayorStaleSeconds > 0 {
			staleStr = color(ansiRed, fmt.Sprintf("mayor: %ds", rig.MayorStaleSeconds))
		}

		// Mayor health.
		mayorStr := "healthy"
		if rig.MayorStaleSeconds > 0 {
			mayorStr = color(ansiRed, "STALE")
		}

		// Pool column: actual / desired
		poolStr := fmt.Sprintf("%d / %d", rig.PoolActual, rig.PoolDesired)

		b.WriteString(fmt.Sprintf("%-*s %-*s %-*s %-*s %-*s %-*s\n",
			colRig, truncate(rig.Name, colRig),
			colStatus, truncate(rig.Status, colStatus),
			colMayor, mayorStr,
			colPolecats, poolStr,
			colScore, scoreStr,
			colStale, staleStr,
		))
	}

	// Custom roles.
	if len(r.CustomRoles) > 0 {
		b.WriteString("\nCUSTOM ROLES\n")
		for _, cr := range r.CustomRoles {
			label := fmt.Sprintf("  %s/%s[%d]", cr.Rig, cr.Role, cr.Instance)
			staleStr := "ok"
			if cr.StaleSeconds > 0 {
				staleStr = color(ansiRed, fmt.Sprintf("last seen: %ds ago", cr.StaleSeconds))
			}
			statusStr := cr.Status
			if cr.StaleSeconds > 0 {
				statusStr = color(ansiRed, "STALE")
			}
			b.WriteString(fmt.Sprintf("%-30s %-10s %s\n", label, statusStr, staleStr))
		}
	}

	// Open Beads.
	if len(r.OpenBeads) > 0 {
		b.WriteString("\nOPEN BEADS\n")
		for _, bead := range r.OpenBeads {
			line := fmt.Sprintf("  %-6s P%-1d   %d open    oldest: %s",
				bead.Type,
				bead.Priority,
				bead.Count,
				formatDuration(time.Duration(bead.OldestSeconds)*time.Second),
			)
			if bead.Escalation && bead.EscalationNote != "" {
				line += " ← ESCALATION: " + bead.EscalationNote
				line = color(ansiRed, line)
			} else if bead.Escalation {
				line = color(ansiRed, line)
			}
			b.WriteString(line + "\n")
		}
	}

	// Cost.
	if len(r.Cost) > 0 {
		b.WriteString("\nCOST (24h)\n")
		for _, c := range r.Cost {
			pctStr := fmt.Sprintf("%.0f%%", c.Pct)
			line := fmt.Sprintf("  %-12s $%.2f / $%.2f   %s",
				truncate(c.Rig, 12),
				c.SpendUSD,
				c.BudgetUSD,
				pctStr,
			)
			switch {
			case c.Pct > 100:
				line = color(ansiRed, line)
			case c.Pct > 90:
				line = color(ansiYellow, line+" ⚠")
			}
			b.WriteString(line + "\n")
		}
	}

	return b.String()
}

// FormatStatusJSON marshals r as a JSON byte slice with version:1 schema.
func FormatStatusJSON(r *StatusResult) ([]byte, error) {
	return json.Marshal(r)
}

// StatusExitCode returns the recommended process exit code for a status command:
//   - 0: all rigs are fully converged (every rig score == 1.0, or no rigs)
//   - 2: at least one rig has a score < 1.0
func StatusExitCode(r *StatusResult) int {
	for _, rig := range r.Rigs {
		if rig.Score < 1.0 {
			return 2
		}
	}
	return 0
}

// truncate returns s truncated to maxLen characters; last char replaced with
// '…' when truncation occurs.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 1 {
		return s[:maxLen]
	}
	return s[:maxLen-1] + "…"
}

// formatDuration formats d as a short human-readable string (e.g. "3m", "2h", "127s").
func formatDuration(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
}
