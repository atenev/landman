package observer_test

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/tenev/dgt/pkg/observer"
	"github.com/tenev/dgt/pkg/surveyor"
)

// gaugeValue reads the current float64 value of a GaugeVec with the given
// label values. It fails the test if the gauge cannot be read.
func gaugeValue(t *testing.T, gv *prometheus.GaugeVec, lvs ...string) float64 {
	t.Helper()
	g, err := gv.GetMetricWithLabelValues(lvs...)
	if err != nil {
		t.Fatalf("GetMetricWithLabelValues(%v): %v", lvs, err)
	}
	var m dto.Metric
	if err := g.Write(&m); err != nil {
		t.Fatalf("Write metric: %v", err)
	}
	return m.GetGauge().GetValue()
}

// ─── RegisterMetrics ────────────────────────────────────────────────────────

func TestRegisterMetrics_AllCollectorsRegistered(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := observer.RegisterMetrics(reg)
	if m == nil {
		t.Fatal("RegisterMetrics returned nil")
	}
	if m.AgentStalenessSeconds == nil {
		t.Error("AgentStalenessSeconds is nil")
	}
	if m.PoolSizeDesired == nil {
		t.Error("PoolSizeDesired is nil")
	}
	if m.PoolSizeDelta == nil {
		t.Error("PoolSizeDelta is nil")
	}
}

func TestRegisterMetrics_Idempotent(t *testing.T) {
	reg := prometheus.NewRegistry()
	m1 := observer.RegisterMetrics(reg)
	m2 := observer.RegisterMetrics(reg) // should not panic
	if m1.AgentStalenessSeconds != m2.AgentStalenessSeconds {
		t.Error("idempotent call should return the same collector instances")
	}
}

// ─── UpdateMetrics: AgentStalenessSeconds ───────────────────────────────────

func TestUpdateMetrics_AgentStalenessSeconds(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := observer.RegisterMetrics(reg)

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	lastSeen := now.Add(-30 * time.Second)

	snap := observer.TopologySnapshot{
		Actual: surveyor.ActualTopology{
			Agents: []surveyor.AgentState{
				{RigName: "rig-a", Role: "mayor", Status: "running", LastSeen: lastSeen},
			},
		},
	}

	observer.UpdateMetrics(m, snap, surveyor.DesiredTopology{}, now)

	got := gaugeValue(t, m.AgentStalenessSeconds, "rig-a", "mayor")
	want := 30.0
	if got != want {
		t.Errorf("AgentStalenessSeconds{rig-a, mayor} = %.1f, want %.1f", got, want)
	}
}

func TestUpdateMetrics_AgentStaleness_MultipleAgents(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := observer.RegisterMetrics(reg)

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	snap := observer.TopologySnapshot{
		Actual: surveyor.ActualTopology{
			Agents: []surveyor.AgentState{
				{RigName: "rig-a", Role: "mayor", LastSeen: now.Add(-10 * time.Second)},
				{RigName: "rig-a", Role: "witness", LastSeen: now.Add(-90 * time.Second)},
				{RigName: "rig-b", Role: "mayor", LastSeen: now.Add(-5 * time.Second)},
			},
		},
	}

	observer.UpdateMetrics(m, snap, surveyor.DesiredTopology{}, now)

	cases := []struct {
		rig, role string
		want      float64
	}{
		{"rig-a", "mayor", 10},
		{"rig-a", "witness", 90},
		{"rig-b", "mayor", 5},
	}
	for _, tc := range cases {
		got := gaugeValue(t, m.AgentStalenessSeconds, tc.rig, tc.role)
		if got != tc.want {
			t.Errorf("AgentStalenessSeconds{%s,%s} = %.1f, want %.1f", tc.rig, tc.role, got, tc.want)
		}
	}
}

// ─── UpdateMetrics: pool delta ───────────────────────────────────────────────

func TestUpdateMetrics_PoolDelta_DesiredMinusActual(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := observer.RegisterMetrics(reg)

	now := time.Now()

	// rig-a: desired=5 polecats, actual=3 active worktrees → delta=2
	snap := observer.TopologySnapshot{
		Actual: surveyor.ActualTopology{
			Worktrees: []surveyor.WorktreeState{
				{RigName: "rig-a", Status: "active"},
				{RigName: "rig-a", Status: "active"},
				{RigName: "rig-a", Status: "active"},
				{RigName: "rig-a", Status: "idle"}, // idle: not counted
			},
		},
	}
	desired := surveyor.DesiredTopology{
		Rigs: []surveyor.DesiredRig{
			{Name: "rig-a", Enabled: true, MaxPolecats: 5},
		},
	}

	observer.UpdateMetrics(m, snap, desired, now)

	wantDesired := 5.0
	wantActual := 3.0
	wantDelta := 2.0

	if got := gaugeValue(t, m.PoolSizeDesired, "rig-a"); got != wantDesired {
		t.Errorf("PoolSizeDesired{rig-a} = %.1f, want %.1f", got, wantDesired)
	}
	if got := gaugeValue(t, m.PoolSizeActual, "rig-a"); got != wantActual {
		t.Errorf("PoolSizeActual{rig-a} = %.1f, want %.1f", got, wantActual)
	}
	if got := gaugeValue(t, m.PoolSizeDelta, "rig-a"); got != wantDelta {
		t.Errorf("PoolSizeDelta{rig-a} = %.1f, want %.1f", got, wantDelta)
	}
}

func TestUpdateMetrics_PoolDelta_FullyProvisioned(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := observer.RegisterMetrics(reg)

	now := time.Now()

	snap := observer.TopologySnapshot{
		Actual: surveyor.ActualTopology{
			Worktrees: []surveyor.WorktreeState{
				{RigName: "rig-b", Status: "active"},
				{RigName: "rig-b", Status: "active"},
			},
		},
	}
	desired := surveyor.DesiredTopology{
		Rigs: []surveyor.DesiredRig{
			{Name: "rig-b", Enabled: true, MaxPolecats: 2},
		},
	}

	observer.UpdateMetrics(m, snap, desired, now)

	if got := gaugeValue(t, m.PoolSizeDelta, "rig-b"); got != 0.0 {
		t.Errorf("PoolSizeDelta{rig-b} = %.1f, want 0.0 (fully provisioned)", got)
	}
}

func TestUpdateMetrics_PoolDelta_OverProvisioned(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := observer.RegisterMetrics(reg)

	now := time.Now()

	// More active worktrees than desired (over-provisioned).
	snap := observer.TopologySnapshot{
		Actual: surveyor.ActualTopology{
			Worktrees: []surveyor.WorktreeState{
				{RigName: "rig-c", Status: "active"},
				{RigName: "rig-c", Status: "active"},
				{RigName: "rig-c", Status: "active"},
			},
		},
	}
	desired := surveyor.DesiredTopology{
		Rigs: []surveyor.DesiredRig{
			{Name: "rig-c", Enabled: true, MaxPolecats: 2},
		},
	}

	observer.UpdateMetrics(m, snap, desired, now)

	// Delta is negative when over-provisioned.
	if got := gaugeValue(t, m.PoolSizeDelta, "rig-c"); got != -1.0 {
		t.Errorf("PoolSizeDelta{rig-c} = %.1f, want -1.0 (over-provisioned)", got)
	}
}
