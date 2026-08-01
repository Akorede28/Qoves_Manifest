# Evidence

Proof for each of the brief's **Done when** clauses, captured as pasted text
rather than screenshots so every artifact is greppable, diffable, and reviewable
without opening an image. All files are UTF-8; `.gitattributes` enforces LF.

Timestamps differ between files because they were captured as each piece of work
completed, not in one batch. Where a file predates a later change, that is noted
below.

| File                              | What it Shows                                                                                          | What it Satisfies |
| --------------------------------- | ------------------------------------------------------------------------------------------------------ | ----------------- |
| `evidence/01-argocd-app-tree.txt` | 8 Applications Synced + Healthy, their source paths, the root app's owned resources, finalizers on all | B                 |

|
| `evidence/02-kubectl-get-all.txt` | `kubectl get pods,svc,ingress,netpol -A` — the exact command the brief names | A, B, D, submission |
|
| `evidence/03-healthz-200-via-ingress.txt` | `curl -si http://qoves.local/healthz` → `200 OK`, body `ok` | C, D, submission |
|
| `evidence/04-netpol-egress-blocked.txt` | Egress to a hostname _and_ to a raw IP, both timing out | D, submission |
|
| `evidence/05-netpol-db-blocked-dns-ok.txt` | DNS resolving while `nc postgres 5432` fails from a non-API pod | D, submission |
|
| `evidence/06-persistence-test.txt` | Row written, `postgres-0` deleted, both rows returned from the same PV | C, F |
|
| `evidence/07-hpa-targets.txt` | HPA at 70% CPU, min 2 / max 5, the requests it is computed against, live usage | G |
|
| `evidence/08-app-database-up.json` | Prometheus query `app_database_up` = 1 on both API replicas | H |
|
| `evidence/09-alert-rules-inactive.json` | Both alert rules registered, `state: inactive`, `health: ok`, with expressions | H |
|
| `evidence/10-secret-never-in-git.txt` | Deliver the database credentials without committing them to git in plaintext | E |

---

## A — Cluster & CNI

> Done when: the cluster is up and you can name the CNI and why.

`02` shows both nodes' workloads, including `calico-node` on each and `calico-kube-controllers`. The reasoning is **ADR-1** in `WRITEUP.md`.

The more useful proof is not that Calico is installed but that it enforces which is what `04` and `05` demonstrate. On minikube's default CNI those same policies would apply cleanly and do nothing, so an installed-CNI screenshot
would prove the opposite of what it appears to.

## B — GitOps delivery

> Done when: the stack is deployed by the controller, not imperative kubectl;
> changing a value in git and syncing changes the cluster; the repo layout is
> legible.

`01` lists all eight Applications, each `Synced` and `Healthy`, each pointing at a directory in this repo, with `resources-finalizer.argocd.argoproj.io` on every one so that pruning deletes rather than orphans.

For changing a value in git changes the cluster, read the commit history each of these was made in git and reconciled by ArgoCD with no imperative apply:

- `Remove the postgres livenessProbe; widen readiness to a 10s timeout`
- `api: raise CPU request to 100m, set explicit probe timeouts`
- `Add resources-finalizer to all Applications so prune deletes rather than orphans`
- `Fix sealed-secrets Helm repoURL: bitnami-labs org migrated to bitnami`
- `Revert "fix: Cutover the API against CNPG through the pg-rw service", a rollback performed as a `git revert`, with zero downtime (ADR-3)

The `Last Sync` timestamps in `01` correspond to those commits.

**The one exception, stated plainly:** the root Application lives in
`bootstrap/`, not in `apps/`, so nothing reconciles it — changing it in git
does nothing until it is re-applied. This is a deliberate trade, discussed in
section 1 of `WRITEUP.md`.

## C — App & database

> Done when: /healthz returns 200 (DB reachable) and the database's data
> survives a pod restart.

`03` — `HTTP/1.1 200 OK`, `Content-Length: 3`, body `ok`, through the ingress on
`qoves.local`.

`06` — a row is written, `postgres-0` is **deleted outright** rather than waiting
for a rolling update, the StatefulSet recreates it, and both rows are selected
back from the replacement pod bound to the same PV.

## D — Networking & least privilege

> Done when: the app is reachable through the ingress with policies applied; a
> pod in the namespace cannot reach something you did not allow (show it); DNS
> still resolves. Access is via the ingress, not port-forward or a raw
> LoadBalancer.

`02` shows the five policies in namespace `app`; `default-deny-all`, `allow-dns-egress`, `api-ingress`, `api-egress-to-postgres`,
`postgres-ingress-from-api`, and the Ingress on `qoves.local` with `ingressClassName: nginx`. There is no `LoadBalancer` Service anywhere in the namespace.

`04` runs from `netpol-test`, a busybox pod in namespace `app` that does **not** carry `app.kubernetes.io/name=api`, so no allow rule matches it:

- `wget http://example.com`, resolves to `172.66.147.243`, then times out.
- `wget http://1.1.1.1`, times out on a raw IP, with no name lookup involved.

The pair matters. The hostname case shows DNS egress is permitted while the connection is not; the raw-IP case rules out "DNS just failed" as an alternative explanation. The block is attributable to the network layer.

`05`, same pod: `nslookup postgres.app.svc.cluster.local` resolves (to a pod IP, since the Service is headless), and `nc -w 5 postgres 5432` exits 1. Name
resolution works, the connection does not.

The positive control is `03`, the API is allowed to reach Postgres, on every readiness probe, and `/healthz` returning 200 through the ingress is the proof. There is no exec-based demonstration of the allowed path because the API image is distroless and has no shell (ADR-5).

## E — Secrets

> Done when: the credential is absent from the repo, the app consumes it at
> runtime, and you can explain how a production store would slot in.

The artifact here is the repository itself rather than a captured command:

- `manifests/secrets/app-db-credentials.sealed.yaml` contains only
  `encryptedData`. It is committed as text, so a reviewer can read it and
  confirm that, an earlier revision was accidentally committed as UTF-16, which
  git classified as binary and refused to diff; that is recorded in ADR-2.
- No commit in the history contains a plaintext or base64 credential. Every
  reference to `DATABASE_URL` in a manifest is a `secretKeyRef`.
- `02` shows the Secret existing in-cluster, materialised by the controller from
  that ciphertext, and `manifests/api/api.yaml` shows the API consuming it by
  reference at runtime.

How a production store slots in: **ADR-2** and Production gaps item 3.

## F — Storage & data

> Done when: the PVC is bound, data persists across a restart, and the writeup
> answers the three questions.

`06` and `07` both show `data-postgres-0` **Bound**, `1Gi`, `RWO`, StorageClass `standard`. `06` is the persistence proof described under C.

The three questions, access mode and its scheduling constraint, what happens when a pod or node dies, and how to back up and restore, are answered in section 3 of `WRITEUP.md`, along with an observed reclaim failure: two PersistentVolumes stayed `Released` with a `Delete` reclaim policy after their claims were removed, and had to be deleted by hand.

## G — Resources & scaling

> Done when: workloads have justified requests/limits; an HPA exists; your
> writeup says whether a CPU-based HPA is the right signal for this API and, if
> not, what you would scale on.

`07` shows the HPA (70% CPU, min 2 / max 5), the requests the percentage is computed against, and live usage from metrics-server.

Read those two numbers together. Reported utilisation is ~1% of a 70% target and it halved when I raised the CPU request from 50m to 100m, under identical traffic. A signal that moves when its denominator moves and stays flat when load moves is not measuring the thing worth scaling on. That is the argument in **ADR-4**, and the alternative (requests-in-flight or p95 from `http_request_duration_seconds`, via prometheus-adapter or KEDA) is named there.

Requests and limits are justified inline in `manifests/api/api.yaml` and `manifests/postgres/postgres.yaml`, including why the API's CPU request was raised; it was the fix for probe timeouts, since the scheduler allocates on requests (ADR-7).

## H — Observability

> Done when: metrics are visible (a Grafana panel or a pasted Prometheus query)
> and one alert rule exists with a one-line rationale for why it is actionable.

`08` — the pasted Prometheus query. `app_database_up` = `1` on both API replicas, each labelled with its pod.

`09` — the rules API, showing both alerts registered with `health: ok` and `state: inactive`:

- **`ApiDatabaseUnreachable`**, `max(app_database_up) == 0` for 2m, severity
  `page`. Actionable because it means no API replica can reach Postgres:
  `/healthz` is serving 503, readiness has pulled every pod from the ingress
  rotation, and the service is down for users. `max` rather than `min` is
  deliberate, `min` would page when one replica has a transient problem that
  readiness already handles by removing it.
- **`ApiScrapeTargetsDown`**, `sum(up{job="api"}) == 0 or absent(up{job="api"})`
  for 2m. The safety net for the failure mode where the first alert cannot fire
  because the metric has stopped arriving at all.

## Submission checklist (from _How to submit_)

| Asked for                                  | Where                        |
| ------------------------------------------ | ---------------------------- |
| `kubectl get pods,svc,ingress,netpol -A`   | `evidence/02`                |
| GitOps app tree                            | `evidence/01`                |
| curl of `/healthz` through the ingress     | `evidence/03`                |
| NetworkPolicy-blocks-traffic demonstration | `evidence/04`, `evidence/05` |

---

## What is deliberately not here

Three things a reviewer might look for and not find, with the reason:

**The CNPG migration and its rollback.** No capture, because the workloads no longer exist. The evidence is the git history; the cutover commit, the `git revert`, the teardown commits, plus the manifests that remain in `manifests/postgres-cnpg*/` and `manifests/minio/`, undeployed and referenced by no Application. The incident is written up in the ADR-3 addendum.

**The pre-fix probe failures on the API pods.** The 42 readiness timeouts described in ADR-7 were on a ReplicaSet that has since been replaced, so those events have aged out of the cluster. The current pods carry zero restarts, which is the outcome rather than the evidence. The postgres side of the same story is reproducible from `kubectl describe` on the historical restart counts recorded
in the runbook.

**An egress-control demonstration allowing exactly one external domain.** Not implemented, and ADR-1 says why: Calico OSS has no FQDN-based egress rule, so the honest options are Cilium's `toFQDNs`, a brittle CIDR allow-list, or an egress proxy. My policies allow zero external destinations, which is stricter but is not the same demonstration, and I would rather name the gap than blur it.
