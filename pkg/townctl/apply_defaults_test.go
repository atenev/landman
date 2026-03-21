// Package townctl — white-box unit tests for applyDefaults (dgt-5h5).
//
// These tests live in package townctl (not townctl_test) so they can access
// the unexported applyDefaults method on ApplyOptions.
package townctl

import (
	"os"
	"testing"
)

// TestApplyDefaults_DoltDSN_SkipsComponentDefaults verifies that when DoltDSN
// is non-empty, applyDefaults returns early without setting any component-based
// connection fields (apply.go:58-61).
func TestApplyDefaults_DoltDSN_SkipsComponentDefaults(t *testing.T) {
	opts := ApplyOptions{
		DoltDSN: "root@tcp(127.0.0.1:3306)/gastown?parseTime=true",
		// All component fields intentionally left as zero values.
	}
	opts.applyDefaults()

	if opts.DoltHost != "" {
		t.Errorf("DoltHost should remain empty when DoltDSN is set, got %q", opts.DoltHost)
	}
	if opts.DoltDB != "" {
		t.Errorf("DoltDB should remain empty when DoltDSN is set, got %q", opts.DoltDB)
	}
	if opts.DoltUser != "" {
		t.Errorf("DoltUser should remain empty when DoltDSN is set, got %q", opts.DoltUser)
	}
	if opts.DoltPort != 0 {
		t.Errorf("DoltPort should remain 0 when DoltDSN is set, got %d", opts.DoltPort)
	}
}

// TestApplyDefaults_NoDSN_SetsComponentDefaults verifies that when DoltDSN is
// empty, applyDefaults populates the component connection fields from their
// env-var defaults (or the hardcoded fallbacks when the env vars are unset).
func TestApplyDefaults_NoDSN_SetsComponentDefaults(t *testing.T) {
	// Ensure the env vars that applyDefaults reads are unset so we observe the
	// hardcoded fallback values ("localhost", "gastown", "root").
	for _, key := range []string{"TOWN_CTL_DOLT_HOST", "TOWN_CTL_DOLT_DB", "TOWN_CTL_DOLT_USER"} {
		old, had := os.LookupEnv(key)
		os.Unsetenv(key)
		if had {
			t.Cleanup(func() { os.Setenv(key, old) })
		}
	}

	opts := ApplyOptions{} // all zero
	opts.applyDefaults()

	if opts.DoltHost != "localhost" {
		t.Errorf("DoltHost = %q, want %q", opts.DoltHost, "localhost")
	}
	if opts.DoltDB != "gastown" {
		t.Errorf("DoltDB = %q, want %q", opts.DoltDB, "gastown")
	}
	if opts.DoltUser != "root" {
		t.Errorf("DoltUser = %q, want %q", opts.DoltUser, "root")
	}
}

// TestApplyDefaults_NoDSN_EnvVarOverridesDefault verifies that when DoltDSN is
// empty and a TOWN_CTL_DOLT_HOST env var is set, applyDefaults uses the env var
// value rather than the "localhost" fallback.
func TestApplyDefaults_NoDSN_EnvVarOverridesDefault(t *testing.T) {
	const customHost = "db.example.internal"
	old, had := os.LookupEnv("TOWN_CTL_DOLT_HOST")
	os.Setenv("TOWN_CTL_DOLT_HOST", customHost)
	t.Cleanup(func() {
		if had {
			os.Setenv("TOWN_CTL_DOLT_HOST", old)
		} else {
			os.Unsetenv("TOWN_CTL_DOLT_HOST")
		}
	})

	opts := ApplyOptions{}
	opts.applyDefaults()

	if opts.DoltHost != customHost {
		t.Errorf("DoltHost = %q, want %q (from env var)", opts.DoltHost, customHost)
	}
}
