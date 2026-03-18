# Spec: Dolt in Kubernetes

## StatefulSet Structure

The DoltInstanceController creates a StatefulSet with the following shape:

```yaml
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: <doltinstance-name>
  namespace: <doltinstance-namespace>
spec:
  serviceName: <doltinstance-name>-headless
  replicas: 1                          # MVP: hardcoded, rejects >1
  selector:
    matchLabels:
      app.kubernetes.io/name: dolt
      gastown.io/doltinstance: <name>
  template:
    spec:
      containers:
      - name: dolt
        image: dolthub/dolt:<spec.version>
        command: ["dolt", "sql-server",
                  "--host", "0.0.0.0",
                  "--port", "3306",
                  "--user", "root",
                  "--data-dir", "/var/lib/dolt"]
        ports:
        - containerPort: 3306
        volumeMounts:
        - name: data
          mountPath: /var/lib/dolt
        readinessProbe:
          tcpSocket:
            port: 3306
          initialDelaySeconds: 10
          periodSeconds: 5
        livenessProbe:
          tcpSocket:
            port: 3306
          initialDelaySeconds: 30
          periodSeconds: 10
  volumeClaimTemplates:
  - metadata:
      name: data
    spec:
      storageClassName: <spec.storage.storageClassName>
      accessModes: ["ReadWriteOnce"]
      resources:
        requests:
          storage: <spec.storage.size>
```

**Headless Service** (for stable DNS within the namespace):
```yaml
apiVersion: v1
kind: Service
metadata:
  name: <doltinstance-name>-headless
spec:
  clusterIP: None
  selector:
    gastown.io/doltinstance: <name>
  ports:
  - port: 3306
    targetPort: 3306
```

**ClusterIP Service** (for operator and Surveyor connections):
```yaml
apiVersion: v1
kind: Service
metadata:
  name: <doltinstance-name>
spec:
  type: ClusterIP
  selector:
    gastown.io/doltinstance: <name>
  ports:
  - port: <spec.service.port>
    targetPort: 3306
```

`DoltInstance.status.endpoint` is set to:
`<name>.<namespace>.svc.cluster.local:<spec.service.port>`

## DDL Initialisation

On first startup (when `desired_topology_versions` table does not exist), the controller
creates a Kubernetes Job to run the DDL init SQL:

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: <doltinstance-name>-ddinit-<hash>
spec:
  template:
    spec:
      restartPolicy: OnFailure
      containers:
      - name: dolt-init
        image: dolthub/dolt:<spec.version>
        command: ["sh", "-c", "dolt sql --host $DOLT_HOST --port $DOLT_PORT < /sql/init.sql"]
        env:
        - name: DOLT_HOST
          value: <doltinstance-name>.<namespace>.svc.cluster.local
        - name: DOLT_PORT
          value: "<spec.service.port>"
        volumeMounts:
        - name: sql
          mountPath: /sql
      volumes:
      - name: sql
        configMap:
          name: gastown-dolt-ddl-v<schemaVersion>   # operator-managed ConfigMap
```

The DDL init SQL includes all `desired_topology_*` tables per the schemas from
`declarative-town-topology`, `surveyor-topology-reconciler`, and `custom-roles-schema`
changes. The `actual_topology_*` tables are initialised here too (owned by the Surveyor
change but present from day one in the K8s deployment).

The Job is owned by the DoltInstance (ownerReference) and garbage-collected on success.
The DoltInstanceController sets `status.conditions[DDLInitialized] = true` after the Job
completes successfully. Other controllers gate on this condition.

## Schema Migration on Version Upgrade

When `spec.version` changes (Dolt binary upgrade), the controller:
1. Sets `status.conditions[VersionMigrationRequired] = true`
2. Creates a migration Job running `dolt_migrate_<old>_to_<new>.sql`
3. The StatefulSet image is updated only after the migration Job completes
4. On failure: StatefulSet image is NOT updated, condition stays, operator emits an Event

SQL migration files are embedded in the operator binary via `go:embed`. This means the
operator binary version determines which migrations are available — operators can only
upgrade one minor version at a time (e.g., 0.1 → 0.2, not 0.1 → 0.3 in one step) unless
the migration file covers multiple versions.

## Connection Model

All operator controllers connect to Dolt using the `go-sql-driver/mysql` driver:
```
root:@tcp(<name>.<namespace>.svc.cluster.local:<port>)/gastown
```

No password for the Dolt root user (Dolt's default for single-node deployments). In
production clusters with network policies, restrict access to the Dolt Service to:
- The operator pod
- The Surveyor pod
- Gas Town agent pods (rig pods)

The operator does not configure Dolt authentication. This is a V2 concern when multi-tenant
or shared-cluster deployments require it.

## Dolt Replication — Out of Scope (MVP)

`DoltInstance.spec.replicas` is validated as `Maximum=1` by the admission webhook and the
controller itself. The controller rejects values > 1 with:
```
spec.replicas: Invalid value: 2: replicas > 1 not supported in gastown-operator v0.x;
see https://github.com/gastown/operator/issues/XXX for replication roadmap
```

Replication will be addressed when there is empirical evidence of a Dolt read bottleneck.
The expected inflection point is ~300 concurrent agents polling `actual_topology`.
