// Package controllers implements controller-runtime reconcilers for all four
// Gas Town CRDs: GasTown, Rig, AgentRole, and DoltInstance.
package controllers

import (
	"context"
	"database/sql"
	"fmt"

	// Register the MySQL driver used for Dolt's MySQL-compatible endpoint.
	_ "github.com/go-sql-driver/mysql"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	gasv1alpha1 "github.com/tenev/dgt/pkg/k8s/operator/v1alpha1"
)

// tableVersion represents a (table_name, schema_version) pair written to
// desired_topology_versions at the start of every Dolt transaction (ADR-0003).
type tableVersion struct {
	Table   string
	Version int
}

// DoltConnector is a function type for opening Dolt connections from a
// NamespacedRef. Reconcilers expose this as an optional field so tests can
// inject a fake doltClient without dialling a real MySQL endpoint.
// When nil, the reconciler falls back to openDoltConnectionFromSpec.
type DoltConnector func(ctx context.Context, k8s client.Client, ref gasv1alpha1.NamespacedRef) (*doltClient, error)

// DoltConnectorByName is a function type for opening Dolt connections via a
// GasTown name + namespace lookup. Used by AgentRoleReconciler.
// When nil, the reconciler falls back to openDoltConnection.
type DoltConnectorByName func(ctx context.Context, k8s client.Client, gastownName, ns string) (*doltClient, error)

// doltClient wraps a *sql.DB for Dolt SQL operations.
type doltClient struct {
	db *sql.DB
}

// openDoltConnection resolves the Dolt endpoint for the given GasTown name and
// namespace, then returns a connected doltClient. The caller is responsible for
// closing the returned client.
func openDoltConnection(
	ctx context.Context,
	k8s client.Client,
	gastownName string,
	gastownNamespace string,
) (*doltClient, error) {
	// Fetch the GasTown CR to find its DoltRef.
	var gt gasv1alpha1.GasTown
	if err := k8s.Get(ctx, client.ObjectKey{Name: gastownName}, &gt); err != nil {
		return nil, fmt.Errorf("get gastown %q: %w", gastownName, err)
	}
	doltRef := gt.Spec.DoltRef

	// Fetch the DoltInstance to get its resolved endpoint.
	var doltInst gasv1alpha1.DoltInstance
	if err := k8s.Get(ctx, client.ObjectKey{
		Name:      doltRef.Name,
		Namespace: doltRef.Namespace,
	}, &doltInst); err != nil {
		return nil, fmt.Errorf("get doltinstance %q: %w", doltRef.Name, err)
	}
	if !doltInstanceReady(&doltInst) {
		return nil, fmt.Errorf("doltinstance %q is not ready", doltRef.Name)
	}

	endpoint := doltInst.Status.Endpoint
	if endpoint == "" {
		return nil, fmt.Errorf("doltinstance %q has empty endpoint", doltRef.Name)
	}

	// DSN: root@tcp(<host>:<port>)/gastown?parseTime=true
	dsn := fmt.Sprintf("root@tcp(%s)/gastown?parseTime=true", endpoint)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open dolt connection to %q: %w", endpoint, err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping dolt at %q: %w", endpoint, err)
	}
	return &doltClient{db: db}, nil
}

// Close releases the underlying connection pool.
func (d *doltClient) Close() error {
	return d.db.Close()
}

// upsertTopologyVersions writes (or updates) a row in desired_topology_versions
// for each supplied table. This MUST be called as the first SQL statement in every
// Dolt transaction — ADR-0003 compliance.
func upsertTopologyVersions(ctx context.Context, tx *sql.Tx, versions []tableVersion) error {
	const query = `
INSERT INTO desired_topology_versions (table_name, schema_version, written_by)
VALUES (?, ?, 'gastown-operator')
ON DUPLICATE KEY UPDATE schema_version = VALUES(schema_version), written_by = VALUES(written_by)`
	for _, v := range versions {
		if _, err := tx.ExecContext(ctx, query, v.Table, v.Version); err != nil {
			return fmt.Errorf("upsert topology version for %q: %w", v.Table, err)
		}
	}
	return nil
}

// doltInstanceReady returns true when the DoltInstance has a Ready=True condition.
func doltInstanceReady(inst *gasv1alpha1.DoltInstance) bool {
	for _, c := range inst.Status.Conditions {
		if c.Type == "Ready" && c.Status == "True" {
			return true
		}
	}
	return false
}

// getDoltSecret fetches the API key secret referenced by the GasTown spec, if any.
// Returns nil if no SecretsRef is configured.
func getDoltSecret(
	ctx context.Context,
	k8s client.Client,
	gt *gasv1alpha1.GasTown,
	namespace string,
) (*corev1.Secret, error) {
	if gt.Spec.SecretsRef == nil {
		return nil, nil
	}
	var secret corev1.Secret
	if err := k8s.Get(ctx, client.ObjectKey{
		Name:      gt.Spec.SecretsRef.Name,
		Namespace: namespace,
	}, &secret); err != nil {
		return nil, fmt.Errorf("get secrets ref %q: %w", gt.Spec.SecretsRef.Name, err)
	}
	return &secret, nil
}
