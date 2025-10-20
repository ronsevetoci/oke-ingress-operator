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
## Verify
kubectl get nodes -l role=ingress -o wide
kubectl -n ingress-nginx get endpointslice -l kubernetes.io/service-name=ingress-nginx-controller -o wide
kubectl -n kube-system logs deploy/oke-ingress-operator -f

## Logging
Uses controller-runtime Zap; tweak verbosity with:
--zap-log-level=info|debug|error  --zap-stacktrace-level=error  --zap-encoder=console|json

## Tested with
- OKE 1.34.1
- k8s.io/* v0.34.1, controller-runtime v0.22.3, Go 1.24

## Example
`examples/example-service.yaml` includes a small nginx app and an annotated LB Service.

## License
Apache-2.0
