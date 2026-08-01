# Writeup — Run a Service the QOVES Way

Cluster: minikube (2 nodes, Kubernetes v1.34, docker driver) · CNI: Calico ·
GitOps: ArgoCD app-of-apps · DB: PostgreSQL 16 (StatefulSet + PVC) · Secrets:
Sealed Secrets · Autoscaling: HPA (CPU) · Observability: raw Prometheus + two alerts.

Proof for every claim below is indexed in `docs/EVIDENCE.md` and captured as
pasted text and picture in `docs/evidence/`.

---

## 1. Run it

### Repo layout

```
app/                        # the API (Go source + Dockerfile), built to GHCR by CI
                            #   see app/README.md for the contract and ADR-6 for why Go
.github/workflows/          # build-and-push; tags by commit SHA, prints the digest to pin
bootstrap/                  # the ONLY imperative steps: minikube start, ArgoCD install,
                            #   root-app.yaml, see bootstrap/README.md
apps/                       # ArgoCD child Applications (the app-of-apps children)
manifests/
  namespaces/               # app, monitoring
  secrets/                  # SealedSecret ciphertext ONLY; no plaintext, ever
  postgres/                 # StatefulSet + PVC + headless Service
  api/                      # Deployment (digest-pinned) + Service + Ingress + HPA
  network-policies/         # default-deny + the minimal allow-list
  monitoring/               # Prometheus (RBAC, scrape config, alert rules, Deployment)
  minio/, postgres-cnpg/,   # designed but undeployed, see ADR-3 addendum;
  postgres-cnpg-restore/    #   no Application references them
docs/                       # this writeup + EVIDENCE.md + evidence/
```

### From scratch

`bootstrap/README.md` is the executable version; the shape is:

1. `minikube start --nodes=2 --cni=calico`, plus the `ingress` and
   `metrics-server` addons.
2. Install ArgoCD by hand (the brief's allowed exception).
3. `kubectl apply -f bootstrap/root-app.yaml`; the last imperative apply.
4. Seal the DB credential with `kubeseal`. The plaintext exists only in the
   shell's memory; `--dry-run=client` means it never reaches the API server.
   Commit the ciphertext.
5. ArgoCD reconciles everything else in sync-wave order: namespaces (0) →
   sealed-secrets controller (1) → SealedSecret (2) → postgres (3) → api (4) →
   network-policies (5) → monitoring (6). The waves encode startup ordering
   declaratively — no init containers, no manual sequencing.

Access: `qoves.local` in the hosts file pointing at 127.0.0.1, with
`minikube tunnel` running (Windows docker driver has no routable node IP).

### Making a change (the GitOps flow)

Edit a manifest → commit → push → ArgoCD syncs automatically, with `prune` and
`selfHeal` on. Manual drift is reverted; deleting a file removes the resource.
App code changes take the longer path: push to `app/` → CI builds and pushes to
GHCR → update the digest in `manifests/api/api.yaml` → commit.

This flow was exercised repeatedly, in both directions: routine changes
(upgrading the sealed-secrets chart, fixing its repo URL when the upstream org
migrated, two rounds of probe tuning) and a full **zero-downtime rollback** of
an attempted database migration via `git revert` (ADR-3 addendum).

**One honest seam.** The root Application lives in `bootstrap/`, not in `apps/`,
so nothing reconciles it; it is the one object whose git state and cluster
state can diverge silently. I hit exactly that: I added the ArgoCD finalizer to
every Application manifest in the repo, and the seven that ArgoCD reconciles
picked it up on the next sync while `root` kept reporting
`finalizers: <none>`, because the only thing that writes the root object is
`kubectl apply`. The alternative is to make the root
app self-managing (point it at a directory that includes its own manifest). I
chose not to, because a self-managing root app can delete itself on a bad
commit and leave nothing to reconcile the repair. The cost is that
`bootstrap/` requires a deliberate re-apply, which `bootstrap/README.md` now
says explicitly.

---

## 2. Decisions (ADRs)

### ADR-1: CNI — Calico

**Decision:** Calico. **Alternatives:** kindnet (minikube's default), Cilium.

kindnet is disqualified by this assignment's core requirement: it does not
implement NetworkPolicy, so policies apply cleanly and silently do nothing. A
default-deny that enforces nothing is worse than none, because it looks like
security. Cilium would also work and brings eBPF, Hubble flow visibility, and
DNS-aware egress policy (which the egress-control stretch needs; see below),
but it has more kernel-dependent failure modes under minikube on
Windows/WSL2. Calico is the documented minikube option, enforces both ingress
and egress, and the blocked-traffic tests prove it rather than assuming it
(`docs/evidence/04`, `05`). Boring and verifiable beat interesting here. The
reasoning transfers: on real metal I would make the same call unless flow
observability or DNS-based egress were hard requirements, at which point
Cilium earns its complexity.

**On the egress-control stretch:** the brief asks to allow exactly one external
domain. My policies allow *zero*, which is stricter but does not demonstrate
the same thing, and I want to be precise about why I did not implement it.
Calico OSS has no FQDN-based egress rule; DNS policy is a Calico Enterprise
feature. On this stack "exactly one domain" therefore means one of: Cilium's
`toFQDNs`, a CIDR allow-list (brittle, since CDN address ranges rotate), or an
in-namespace egress proxy that does hostname allow-listing while the policy
permits only the proxy. The proxy is the answer I would ship, because it is the
only one that survives the target changing its IPs.

### ADR-2: Secrets, Sealed Secrets

**Decision:** Sealed Secrets. **Alternatives:** SOPS + age, External Secrets
Operator.

SOPS needs an ArgoCD repo-server plugin, which is an extra moving part in the
delivery path itself. ESO is the right production answer but only against a
real external store; its inline and fake providers put the value back into a
manifest, i.e. into git, which defeats the point. Sealed Secrets is fully
local, needs no ArgoCD modification, and its ciphertext is safe in a public
repo: encryption uses the controller's public cert, decryption requires the
private key that exists only in-cluster. The plaintext credential was generated
in shell memory and piped through `kubeseal`, so it never touched disk, git, or
the API server.

**Incident worth recording (upstream moved):** mid-build, the project migrated
from the `bitnami-labs` GitHub org to `bitnami`, and the old Helm repo URL
began returning 404. ArgoCD surfaced it as a ComparisonError; the fix was a
one-line `repoURL` change in git. Two lessons: pinned versions do not protect
you from upstream *hosting* changes, and this is precisely why production
mirrors third-party charts and images into a registry it controls (Production
gaps, item 4).

**Incident worth recording (encoding):** `kubeseal --format yaml > file.yaml` in
PowerShell writes UTF-16LE. Git sees NUL bytes and classifies the file as
binary, so `git diff` refuses to show it and GitHub will not render it; the
one artifact that proves I committed ciphertext rather than a credential became
the one artifact a reviewer could not read. Re-encoded to UTF-8 (identical
bytes decoded, so the controller never saw a change and the Secret's age is
continuous) and added a `.gitattributes` so it cannot recur. Small, but it is a
good example of a tooling default quietly undermining a security control.

### ADR-3: PostgreSQL, raw StatefulSet (CloudNativePG attempted, rolled back)

**Decision:** raw StatefulSet, single replica, PVC via `volumeClaimTemplates`.
**Alternative:** CloudNativePG.

The raw StatefulSet keeps every line defensible: one container, one volume,
credentials from the sealed Secret, a `pg_isready` readiness probe, `fsGroup`
for volume ownership, and a headless Service because there is exactly one
stateful backend and no load-balancing semantics are wanted. What it does not
give me is everything an operator exists for: replication, failover,
switchover, scheduled backups, PITR, and safe minor upgrades. CNPG is the right
answer the moment any of those are requirements.

**Addendum: the attempted CNPG migration, and what its teardown taught me.**

I attempted the migration through GitOps: the operator as a pinned Helm
Application, MinIO as a local S3 backup target, a `Cluster` with WAL archiving
and a scheduled backup, and a bootstrap-from-backup restore drill. All those
manifests remain in the repo, undeployed.

The operator reconciled correctly, but instance initialization failed with
`initdb: could not create directory: Permission denied`. Root cause: CNPG runs
PostgreSQL strictly non-root and relies on Kubernetes' `fsGroup` for volume
ownership, and minikube's default hostPath provisioner does not apply
`fsGroup`. The raw StatefulSet had masked this all along, because the official
postgres image chowns its own data directory as root before dropping
privileges. The stricter component exposed a latent weakness in the storage
layer beneath it. The documented remediation (minikube's CSI hostpath driver,
which honours `fsGroup`) then failed to schedule its eight pods on an already
resource-constrained cluster.

I time-boxed the attempt and rolled back: a single `git revert` of the API
cutover, with **zero downtime on the served path**, because the probe-gated
rollout had never removed the old pods from the Service. Then an ordered
teardown; `Cluster` CR before the operator, since a live operator is required
to honour the CR's finalizer.

Teardown is where the real lesson was. Child Applications without ArgoCD's
`resources-finalizer` delete only the Application object when pruned; the
workloads survive with nothing in git owning them. I removed the obvious
orphans at the time and believed the cleanup was complete. It was not: the CNPG
operator Deployment, its webhook Service and six CRDs kept running for a
further **18 hours**, and I only found them because I read my own
`kubectl get pods -A` evidence file and saw a namespace that no longer existed
in the repo. Two orphaned PersistentVolumes were also still present, which is
covered in section 3.

The durable fix is in git: `resources-finalizer.argocd.argoproj.io` on every
Application manifest in the repo, so pruning deletes the resources an
Application manages instead of abandoning them
(`docs/evidence/01`). The remnants themselves had to be removed imperatively,
because by definition nothing in git still referenced them; that is the shape
of this class of bug, and it is why the finalizer matters more than the
cleanup did.

On production hardware with a real CSI provisioner the migration proceeds as
designed. The deeper lesson is that operators encode security assumptions the
platform underneath must actually meet, and that a GitOps controller only owns
what you have told it it owns.

### ADR-4: Scaling signal, CPU (deployed) with a documented case against it

**Decision:** HPA on CPU at 70% of requests, min 2 / max 5, because
metrics-server is the assignment's provided path.

**Honest assessment:** CPU is the wrong primary signal for this API. The
handlers are I/O-bound, a request spends its life waiting on Postgres, so
when the database slows down, latency climbs while CPU stays flat and the HPA
correctly does nothing. Worse, if it did scale, extra replicas would only add
connections to a struggling database.

The build makes the point better than the argument does. After I raised the CPU
request from 50m to 100m to fix the probe timeouts (ADR-7), reported
utilisation halved from 5% to **1% of a 70% target**, same traffic, same work,
a number that moved only because its denominator moved. A signal that
responsive to a resource change and that unresponsive to load is not measuring
the thing I care about (`docs/evidence/07`).

The right signal is requests-in-flight or p95 latency, both derivable from the
`http_request_duration_seconds` histogram the app already exports, fed through
prometheus-adapter or KEDA, with the database connection pool as the recognised
true bottleneck. CPU-based HPA is deployed as the required baseline; scaling on
it in production would be the classic mistake the brief alludes to.

### ADR-5: Images, digest pinning + distroless

The API image is referenced by sha256 digest, not tag: immutable, and immune to
tag mutation or deletion. The runtime image is distroless `:nonroot`; no
shell, no package manager. Tradeoff accepted: no `kubectl exec` debugging, so
you debug with logs, `/metrics`, and `kubectl debug` ephemeral containers. That
tradeoff had a visible consequence in this build; the NetworkPolicy
demonstration needed a separate busybox pod, and the proof that the *allowed*
path works is `/healthz` returning 200 through the ingress rather than an exec
into the API.

A real bug this surfaced: `runAsNonRoot: true` plus the image's *named* user
`nonroot` failed kubelet verification with `CreateContainerConfigError`,
because the kubelet can only compare numeric IDs. Fix: pin
`runAsUser`/`runAsGroup: 65532`. The manifest-level constraint stays even
though the image already drops root, because it is enforced by the kubelet
regardless of what a future image claims.

### ADR-6: App implementation; Go, against the provided Python

**Decision:** implement the brief's contract in Go rather than deploy the
provided Flask application. **Alternatives:** ship the provided `app/main.py`
as-is; ship it with Prometheus multiprocess mode configured.

The brief permits a rewrite in another language provided the three endpoints
and the `DATABASE_URL`-from-Secret contract are preserved, and states the app
itself is not scored. Two reasons the rewrite is not gratuitous:

**The provided app's metrics are unreliable as shipped.** Its Dockerfile runs
`gunicorn --workers 2`, and `prometheus_client` keeps its registry per-process.
Without `PROMETHEUS_MULTIPROC_DIR` and a `MultiProcessCollector`, a `/metrics`
scrape returns whichever worker happened to answer, so `http_requests_total`
splits across workers and undercounts non-deterministically. A single-process
Go binary has no such split. This is a real trap rather than a theoretical one:
the assignment asks for observability, and the provided observability is subtly
wrong at the default worker count.

**It exposes no database-health signal.** The provided app exports only
`http_requests_total{path,status}`, so the only available database alert is
inferred from a counter of `/healthz` 503s, alerting on a symptom's proxy
rather than on the condition. My implementation adds `app_database_up`, a
boolean gauge set by the same check `/healthz` performs, which is what makes
`ApiDatabaseUnreachable` a statement about the world rather than about traffic.
It also exports `http_request_duration_seconds` as a histogram, which is the
input the scaling signal in ADR-4 would actually use.

Cost accepted: a reviewer has to read my source to confirm contract parity,
where the provided app would have been trusted on sight. `app/README.md` states
the contract endpoint by endpoint and names `app_database_up` as the one
deliberate addition beyond it.

I also considered vendoring the provided Python alongside the Go, as a
reference copy, and decided against it. An unbuilt, undeployed second
implementation is dead weight, it has to be explained to everyone who finds
it, it will drift from the thing that actually runs, and no CI job would ever
catch that drift. If the contract needs verifying, the README states it and
`/metrics` demonstrates it live; a copy nobody executes proves less than
either. The brief is explicit that gold-plating earns nothing, and code you do
not run is the purest form of it.

### ADR-7: No liveness probe on the database

**Decision:** the PostgreSQL StatefulSet has a readiness probe and **no**
liveness probe. The API keeps both, split by dependency.

This is the decision I got wrong twice before getting it right, so the
reasoning is worth stating carefully.

A liveness probe answers "should the kubelet kill this container?" Restarting
is only a remediation if the failure mode is one a restart repairs. For a
single-replica database, that set is nearly empty: PostgreSQL does not hang in
ways a SIGTERM fixes, and if it is genuinely wedged I want a human and the WAL,
not an automated restart. Meanwhile the probe's cost is unbounded; every
restart is a full outage, because there is no second replica.

Worse, the probe and the process it checks compete for the same resource. On a
loaded node, `pg_isready` must fork, exec and connect before the timeout
expires; the conditions that make a database slow are exactly the conditions
that make its health check slow. A liveness probe under contention does not
detect failure, it manufactures it. This is not hypothetical; see the runbook
below, where it killed a healthy database at both a 1-second and a 5-second
timeout.

Readiness has none of these properties. It gates traffic, not lifecycle. When
`pg_isready` fails, the pod leaves the Service, the API's `/healthz` starts
reporting 503, and the alert fires; every signal I want, and no destruction.

**The API is the control case for this argument.** Its liveness probe hits `/`
(DB-independent) and its readiness probe hits `/healthz` (DB-dependent). Under
the same node pressure that killed postgres-0, the API pods recorded 42
readiness failures in 177 minutes, `context deadline exceeded` on `/healthz`,
correctly removing replicas from the Service, and no liveness failure and no
kubelet kill at all. Same cluster, same hour, same pressure: the probe bound to
a dependency that was starving caused an outage, and the probe bound to
something the process controls locally did not. (Those pods have since been
replaced by the resource change described below, so the events live in the
incident capture rather than in the cluster; the current pods carry zero
restarts.)

**Alternative considered:** `tcpSocket: 5432` as a cheaper liveness check. It
costs the kubelet almost nothing, which addresses the contention problem, but
it passes while PostgreSQL is in crash recovery and not serving queries, so it
would report healthy during precisely the incident I would want it to catch. A
probe that is cheap and wrong is not an improvement.

---

## 3. Storage & data (section F answers)

**Access mode, and what it constrains.** The PVC binds ReadWriteOnce on
minikube's `standard` (hostPath) StorageClass. RWO means one node mounts the
volume read-write; combined with hostPath, the data literally lives on that
node's filesystem, so the pod is pinned to the node once bound. That is the
scheduling constraint: `postgres-0` cannot fail over to the second node,
because its data does not exist there. On real infrastructure the same RWO
semantics apply to network block storage, but the volume can re-attach to a
replacement node; the constraint moves from "this node" to "one node at a
time," which is a much weaker restriction and the reason network-attached
storage is table stakes for stateful workloads.

**If the pod dies.** The StatefulSet recreates it, the PVC re-binds, and the
data survives. Proven by deleting `postgres-0` outright, waiting for the
replacement to pass `pg_isready`, and selecting both rows back from the same PV
(`docs/evidence/06`). The row written before the first probe rollout has now
survived two rolling updates, an apiserver outage, and an explicit pod
deletion.

**If the node dies.** On this cluster the data is gone with the node's disk,
because hostPath is node-local. That is the single biggest gap between this
build and production, and it is why the backup answer matters more than the HA
answer.

**Backup and restore.** Application-level backups rather than volume snapshots,
because they are storage-agnostic and testable: a CronJob running
`pg_dump -Fc` shipped to object storage (MinIO locally, S3 in production), with
retention enforced by the bucket. Restore is the drill that counts; provision
an empty instance, `pg_restore`, point `DATABASE_URL` at it, verify `/healthz`.
The operator path folds all of this into declarative `Backup` and
`ScheduledBackup` CRDs with WAL archiving and PITR, which is a second argument
for CNPG beyond HA and the design I attempted (`manifests/postgres-cnpg*/`,
ADR-3 addendum).

**A reclaim behaviour worth recording.** When I removed the CNPG and MinIO
PersistentVolumeClaims, their PersistentVolumes did not go away. Both sat in
`Released` with a reclaim policy of `Delete`; the claim was gone, the
provisioner had not collected the volume, and the disk space stayed allocated
on the node until I deleted the PVs by hand. Storage that leaks quietly on
delete is the mechanism behind "a volume fills," and it is a good argument for
monitoring PV count and node disk usage rather than trusting a reclaim policy
to have run. On a cluster with real CSI storage this is the difference between
a tidy teardown and a slow, invisible bill.

---

## 4. What minikube did for me

Things this build got for free that bare metal would make me own:

- **Control-plane bootstrap.** `minikube start` ran kubeadm, generated the CA
  and component certificates, and wired kubelet auth. On metal: kubeadm
  init/join, certificate lifecycle, and a live example from this build,
  version-skew management. A stale profile with expired v1.28-era certs could
  not be renewed by a v1.34 kubeadm at all, because its skew policy refuses
  control planes older than v1.33. The fix locally was delete-and-recreate; in
  production that is a planned, one-minor-version-at-a-time upgrade path, and
  the reason certificate expiry belongs on a calendar rather than in an
  incident.
- **CNI install.** `--cni=calico` was one flag. On metal it is choosing and
  operating the CNI: IPAM pools, BGP or VXLAN/IPIP encapsulation, MTU, and
  upgrade coordination with the kubelet.
- **Ingress load-balancing.** The addon deployed ingress-nginx, and
  `minikube tunnel` played the part of a cloud load balancer. On metal there is
  no `LoadBalancer` type at all, you run MetalLB or BGP to real routers, VIP
  failover, and the edge (DNS, TLS termination) yourself.
- **Storage provisioner.** The hostPath provisioner auto-bound the PVC, and
  also bit back twice: it silently ignores `fsGroup`, which is what broke the
  CNPG migration (ADR-3), and it left `Released` volumes uncollected
  (section 3). On metal the storage layer is a first-class engineering choice;
  Ceph, Longhorn, OpenEBS, or a SAN CSI driver, and its semantics (`fsGroup`,
  topology, expansion, snapshots, reclaim) become contracts your workloads
  depend on, as this build demonstrated the hard way.
- **etcd and its backup.** Invisible here; on metal it is the crown jewels.
  Dedicated disks (fsync latency dominates), member health, defragmentation,
  and scheduled snapshots that are tested by actually restoring them, because
  an etcd backup that has never been restored is a hope, not a backup.

---

## 5. Production gaps

What is missing before this serves real traffic, in the order I would fix it:

1. **Backups.** Data loss is the only unrecoverable failure here. `pg_dump`
   CronJob or CNPG scheduled backups plus WAL archiving to object storage, with
   a restore drill on a schedule; see section 3.
2. **Database HA.** A single Postgres replica on node-local storage. The CNPG
   design exists in this repo (ADR-3 addendum); it needs real network-attached
   CSI storage and adequate node resources: three instances, synchronous
   replication, automated failover.
3. **A real secret backend.** The Sealed Secrets private key exists in exactly
   one cluster. Lose the cluster and the ciphertext in git is permanently
   undecryptable; I have no key backup, which is itself the gap. Production:
   External Secrets Operator against Vault or a cloud secrets manager, with
   rotation, plus a tested key-recovery procedure.
4. **Supply chain.** GHCR is external, and two upstream incidents in this one
   build (the sealed-secrets org migration; the CNPG/minikube storage
   interaction) showed how dependencies move underneath you. Mirror charts and
   images into a registry you control; add admission policy so only signed
   images run.
5. **Ingress, TLS, edge.** HTTP only, one nginx replica, tunnel-as-LB. Needs
   TLS via cert-manager, multiple ingress replicas, a real load balancer or
   MetalLB, and rate limiting at the edge.
6. **Upgrades and DR.** etcd snapshots with restore drills, one-minor-version
   upgrade runbooks, and ideally a second cluster so the platform itself is not
   a single point of failure.
7. **Observability depth.** Prometheus here is single-replica with ephemeral
   storage and no Alertmanager — the alerts evaluate correctly and page nobody.
   Production: durable TSDB or remote-write, Alertmanager routing to on-call,
   and SLO-based alerting (error-rate and latency burn rates) layered on top of
   the symptom alerts.
8. **Resource headroom.** Almost every incident in this build traces back to a
   two-node laptop cluster under memory and CPU pressure, including an
   apiserver that stopped holding. That is an artefact of the environment, but
   the lesson transfers: capacity is a reliability property, and probes,
   schedulers and control planes all degrade in ways that look like application
   bugs when it is missing.
9. **Scaling signal.** Replace the CPU HPA per ADR-4.

---

## 6. Runbook, the DB pod dies

**Symptoms.** `ApiDatabaseUnreachable` fires (`max(app_database_up) == 0` for
2m); `/healthz` returns 503; API pods drop out of the ingress rotation via
readiness, so users see errors at the edge. API pods do **not** restart, by
design, liveness is DB-independent, because restarting the API cannot fix the
database (ADR-7).

Note on the alert expression: `max()` fires only when *no* replica can reach
Postgres, which is a database outage. `min()` would page when a single replica
has a transient connection problem that readiness already handles by removing
it from the Service. The paired rule `ApiScrapeTargetsDown`
(`sum(up{job="api"}) == 0 or absent(up{job="api"})`) covers the failure mode
where the first alert cannot fire because the metric has stopped arriving at
all.

**Diagnose (read-only, no mutations):**

```
kubectl get pods -n app                       # postgres-0 status and restart count
kubectl describe pod postgres-0 -n app        # events: OOMKilled? probe failures? unschedulable?
kubectl logs postgres-0 -n app --previous     # why the last container died
kubectl get pvc -n app                        # data-postgres-0 still Bound?
kubectl get events -n app --sort-by=.lastTimestamp
```

Read the events, not just the status. `Last State: Terminated: Completed,
exit 0` looks like a clean shutdown and is frequently the kubelet killing a
healthy process; see the incident below.

**Expected self-heal.** The StatefulSet recreates the pod, the PVC re-binds,
`pg_isready` gates readiness, the API's next `/healthz` probe succeeds, pods
re-enter the rotation, and the alert resolves. Verify the data survived:

```
kubectl exec -n app postgres-0 -- psql -U app -d app -c "SELECT count(*) FROM persistence_test;"
```

**If it does not self-heal, by cause, through git wherever the fix is config:**

- **OOMKilled.** Raise the memory limit in `manifests/postgres/postgres.yaml`,
  commit, push; ArgoCD rolls it out.
- **CrashLoop on corrupt PGDATA.** Do not fight it in place. The API is already
  degrading to 503, so the outage cost is paid: provision a fresh volume
  (delete the PVC and let the template re-provision), `pg_restore` the latest
  dump, verify `/healthz`.
- **Unschedulable because the node is gone.** On this cluster the data was
  node-local, so this *is* the restore-from-backup path above. Acknowledge the
  RPO and say so in the incident review.
- **Bad recent change.** `git revert` the offending commit and let ArgoCD
  reconcile backwards. Rollback is a git operation, not a kubectl one.

### This runbook was exercised for real, twice

**Round one.** During the CNPG attempt's resource pressure, `postgres-0`
accumulated 40 restarts, yet every `Last State` read
`Terminated: Completed, exit 0`, which looks like a clean shutdown rather than
a crash. The events told the truth: `Liveness probe failed: command timed out`.
The `pg_isready` exec probes were running with the default `timeoutSeconds: 1`;
on a CPU-starved node, fork plus exec plus connect exceeded one second, three
liveness misses accrued, and the kubelet SIGTERMed a perfectly healthy
database, hence the graceful exit 0. Postgres was being killed by its own
health check. Fix through git: `timeoutSeconds: 5` on both probes and
`failureThreshold: 6` on liveness, so a stateful pod needs roughly 60 seconds
of sustained failure before the kubelet acts.

**Round two, which is the part that matters.** The restarts did not stop. A day
later the same pod had restarted five more times, with events reading
`pg_isready ... timed out after 5s`, four liveness and seven readiness
timeouts in 93 minutes, and the same graceful `exit 0` signature. I had fixed
the symptom and misdiagnosed the cause. One second was genuinely too tight, but
the real problem was never the threshold: the probe and the database contend
for the same starved CPU, so *no* timeout value is safe. Widening it only moves
the failure further out.

The actual fix was to delete the liveness probe (ADR-7) and widen readiness to
a 10-second timeout. Restart counters have been flat at 0 on all three pods
since, including across a deliberate `kubectl delete pod postgres-0`, which
doubled as the data-persistence proof (`docs/evidence/06`). The API, whose
liveness probe was bound to something the
process controls locally rather than to the database, never restarted at all
through either episode.

The transferable lesson is about diagnosis, not probes. A fix that reduces a
symptom's frequency is indistinguishable from a fix that addresses its cause
until you look again, and I only looked again because the restart count in an
evidence file contradicted a sentence in this document.

**Post-incident.** Capture the timeline from Prometheus (`app_database_up` over
the window), attach `kubectl describe` and logs, and file the gap it exposed.

---

## Self-check (from the brief)

- **Reconciled from git.** The whole stack is deployed by ArgoCD. The only
  imperative operations were cluster bootstrap (allowed), and removal of the
  CNPG remnants described in ADR-3, which had to be imperative, because
  nothing in git referenced them any longer. That is the failure the
  finalizer commit prevents from recurring.
- **NetworkPolicy blocks something, tested.** Egress to `example.com` and to a
  raw IP both time out; a non-API pod cannot open `postgres:5432`; DNS still
  resolves; the ingress path still serves 200 (`docs/evidence/04`, `05`).
- **No secret in the repo.** Plaintext or base64, in any commit. The only
  committed artifact is SealedSecret ciphertext, and it is now readable as text
  so that can be verified rather than taken on trust.
- **Images pinned.** API by sha256 digest, postgres and prometheus by version
  tag, Helm chart by version. No `:latest` anywhere.
- **`/healthz` returns 200 through the ingress; data survives a pod restart**
  (`docs/evidence/03`, `06`).
- **This writeup covers the five required sections** plus the section-F storage
  answers.
