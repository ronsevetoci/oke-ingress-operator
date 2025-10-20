# OKE Ingress Combined Operator

A focused operator for **Oracle Container Engine for Kubernetes (OKE)** that solves this challenge:

> **Keep OCI Load Balancer backend sets aligned with the *right* nodes running nginx-ingress, even as pods move and when `externalTrafficPolicy: Local` is used.**

### What it does
1. **Labeler controller** — Watches **EndpointSlice** for your ingress Service and labels nodes that currently host **Ready** endpoints (default `role=ingress`); removes label when not hosting.
2. **Backend-sync controller** — Watches annotated `LoadBalancer` Services and “pokes” the OCI **CCM** by patching a timestamp annotation to force a backend-set refresh. With `ETP: Local`, it intersects labeled nodes with nodes that actually host endpoints.

### Why this matters
- Avoids drift between Service intent and OCI LB backends
- Works with node churn, autoscaling, and ETP=Local
- Clear logs for operability and fast troubleshooting

---

### Deploy 
git clone https://github.com/ronsevetoci/oke-ingress-operator.git
cd oke-ingress-operator

helm install oke-ingress-operator ./charts/oke-ingress-operator \
  --namespace kube-system

## Verify
kubectl get nodes -l role=ingress -o wide
kubectl -n ingress-nginx get endpointslice -l kubernetes.io/service-name=ingress-nginx-controller -o wide
kubectl -n kube-system logs deploy/oke-ingress-operator -f

## Tested with
- OKE 1.34.1
- k8s.io/* v0.34.1, controller-runtime v0.22.3, Go 1.24

## Nginx optimal setup for this scenario with mitigations
helm upgrade --install ingress-nginx ingress-nginx/ingress-nginx \
  --namespace ingress-nginx --create-namespace \
  \
  --set controller.replicaCount=3 \
  --set controller.updateStrategy.rollingUpdate.maxUnavailable=0 \
  --set controller.updateStrategy.rollingUpdate.maxSurge=1 \
  --set controller.lifecycle.preStop.exec.command='{"/bin/sh","-c","sleep 10"}' \
  \
  --set controller.service.type=LoadBalancer \
  --set controller.service.externalTrafficPolicy=Local \
  --set controller.service.annotations."oci\.oraclecloud\.com/node-label-selector"="role=ingress" \
  \
  --set 'controller.topologySpreadConstraints[0].maxSkew=1' \
  --set 'controller.topologySpreadConstraints[0].topologyKey=kubernetes.io/hostname' \
  --set 'controller.topologySpreadConstraints[0].whenUnsatisfiable=ScheduleAnyway' \
  --set 'controller.topologySpreadConstraints[0].labelSelector.matchLabels.app\.kubernetes\.io/name=ingress-nginx' \
  --set 'controller.topologySpreadConstraints[0].labelSelector.matchLabels.app\.kubernetes\.io/component=controller' \
  \
  --set 'controller.affinity.podAntiAffinity.preferredDuringSchedulingIgnoredDuringExecution[0].weight=100' \
  --set 'controller.affinity.podAntiAffinity.preferredDuringSchedulingIgnoredDuringExecution[0].podAffinityTerm.topologyKey=kubernetes.io/hostname' \
  --set 'controller.affinity.podAntiAffinity.preferredDuringSchedulingIgnoredDuringExecution[0].podAffinityTerm.labelSelector.matchLabels.app\.kubernetes\.io/name=ingress-nginx' \
  --set 'controller.affinity.podAntiAffinity.preferredDuringSchedulingIgnoredDuringExecution[0].podAffinityTerm.labelSelector.matchLabels.app\.kubernetes\.io/component=controller'
  
## License
Apache-2.0
