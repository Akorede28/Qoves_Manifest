# Qoves_Manifest

GitOps platform for the QOVES take-home: (minikube, 2 nodes, Calico), ArgoCD app-of-apps,
PostgreSQL + PVC, default-deny NetworkPolicies, Sealed Secrets, HPA, Prometheus.

- **[docs/WRITEUP.md](docs/WRITEUP.md)** the five required sections plus seven ADRs
- **[docs/EVIDENCE.md](docs/EVIDENCE.md)** every "Done when" clause mapped to a captured artifact
- **[bootstrap/README.md](bootstrap/README.md)** how to stand it up from scratch

`bootstrap/` holds the only imperative steps (cluster, ArgoCD, root Application).
Everything after that is reconciled from `apps/` and `manifests/` by ArgoCD.
`app/` is the API itself; see [app/README.md](app/README.md) for the contract.
