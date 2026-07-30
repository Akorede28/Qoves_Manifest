# Bootstrap (the only imperative steps)

Everything in this file is run **once**, by hand. It is the explicitly-allowed
exception in the brief: "Installing the controller itself by hand or Helm is
fine - only what comes after must be GitOps-managed." After step 4, the cluster
is changed exclusively through git commits.

All commands are PowerShell.

## 1. Cluster + CNI (section A)

Pick ONE based on host RAM:

```powershell
# >= 16 GB host RAM - two nodes (more realistic scheduling)
minikube start --nodes=2 --cni=calico --memory=3500 --cpus=2 --driver=docker

# < 16 GB host RAM - single node (accepted by the brief; limitation noted in writeup)
minikube start --cni=calico --memory=4096 --cpus=2 --driver=docker
```

Why Calico: minikube's default CNI (kindnet) does not enforce NetworkPolicy -
policies apply cleanly and silently do nothing. Calico is the documented
minikube CNI option that enforces both ingress and egress policy. (Full ADR in
docs/decisions.)

Verify:

```powershell
kubectl get nodes
kubectl get pods -n kube-system -l k8s-app=calico-node
```

## 2. Addons

```powershell
minikube addons enable ingress
minikube addons enable metrics-server
```

## 3. ArgoCD (hand-install allowed)

```powershell
kubectl create namespace argocd
kubectl apply -n argocd -f https://raw.githubusercontent.com/argoproj/argo-cd/v2.13.3/manifests/install.yaml
kubectl -n argocd wait --for=condition=Available deployment/argocd-server --timeout=300s
```

UI access (optional; port-forward is fine for the _ArgoCD UI_ - the ban on
port-forward is about serving the app, which goes through the ingress):

```powershell
kubectl -n argocd port-forward svc/argocd-server 8443:443
# password:
kubectl -n argocd get secret argocd-initial-admin-secret -o jsonpath="{.data.password}" | %{ [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($_)) }
# login: https://localhost:8443  user: admin
```

## 4. Root app - the last kubectl apply ever

```powershell
kubectl apply -f bootstrap/root-app.yaml
```

Deleting the root app now cascades through every child. Use kubectl delete -f bootstrap/root-app.yaml --cascade=orphan if you ever need to re-bootstrap without tearing down state.

From here on, ArgoCD reconciles the cluster to match the apps/ directory of
this repo. All changes = git commit + push.

## 5. Seal the database credential (one-time, values never touch git)

Install kubeseal CLI (Windows):

```powershell
# download kubeseal from the sealed-secrets release page matching chart v2.17.x
# https://github.com/bitnami-labs/sealed-secrets/releases
# put kubeseal.exe on PATH, then:
kubeseal --version
```

Generate a strong password and build the secret LOCALLY (never committed;
--dry-run means the plaintext Secret never even hits the API server):

```powershell
$pw = -join ((48..57)+(65..90)+(97..122) | Get-Random -Count 32 | %{[char]$_})

kubectl create secret generic app-db-credentials `
  --namespace app `
  --from-literal=POSTGRES_USER=app `
  --from-literal=POSTGRES_PASSWORD=$pw `
  --from-literal=POSTGRES_DB=app `
  --from-literal=DATABASE_URL="postgres://app:$pw@postgres.app.svc.cluster.local:5432/app?sslmode=disable" `
  --dry-run=client -o yaml | kubeseal --format yaml > manifests/secrets/app-db-credentials.sealed.yaml

git add manifests/secrets/app-db-credentials.sealed.yaml
git commit -m "Sealed database credential (ciphertext only)"
git push
```

The SealedSecret ciphertext is safe in a public repo: only the controller's
private key (in-cluster) can decrypt it.

## 6. Reach the app through the ingress

On the Windows docker driver, ingress needs the tunnel (run as Administrator,
keep it open):

```powershell
minikube tunnel
```

Add to C:\Windows\System32\drivers\etc\hosts (as Administrator):

```
127.0.0.1 qoves.local
```

Then:

```powershell
curl.exe -i http://qoves.local/healthz
```

Done when: HTTP 200 body "ok" - through the ingress, backed by a live
SELECT 1 against Postgres.
