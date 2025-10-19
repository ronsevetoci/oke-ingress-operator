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

## Install (Helm)
```bash
helm upgrade --install oke-ingress-combined charts/oke-ingress-combined-operator   -n kube-system --create-namespace   --set image.repository=ocir.eu-frankfurt-1.oci.oraclecloud.com/frsxwtjslf35/oke-ingress-combined-operator   --set image.tag=0.3.0   --set controllers.enableLabeler=true   --set controllers.enableBackendSync=true   --set labeler.ingressNamespace=ingress-nginx   --set labeler.ingressService=ingress-nginx-controller   --set labeler.labelKey=role   --set labeler.labelValue=ingress
```

Annotate your `LoadBalancer` Service:
```yaml
metadata:
  annotations:
    oci.oraclecloud.com/node-label-selector: "role=ingress"
```

## Verify
```bash
kubectl get nodes -l role=ingress -o wide
kubectl -n ingress-nginx get endpointslice -l kubernetes.io/service-name=ingress-nginx-controller -o wide
kubectl -n kube-system logs deploy/oke-ingress-combined-operator -f
```

## Build & Push (Podman → OCIR)
```bash
podman build --platform=linux/amd64   -t ocir.eu-frankfurt-1.oci.oraclecloud.com/frsxwtjslf35/oke-ingress-combined-operator:0.3.0 .
podman login ocir.eu-frankfurt-1.oci.oraclecloud.com
podman push ocir.eu-frankfurt-1.oci.oraclecloud.com/frsxwtjslf35/oke-ingress-combined-operator:0.3.0
```

## Logging
Uses controller-runtime Zap; tweak verbosity with:
```
--zap-log-level=info|debug|error  --zap-stacktrace-level=error  --zap-encoder=console|json
```

## Tested with
- OKE 1.34.1
- k8s.io/* v0.34.1, controller-runtime v0.22.3, Go 1.24

## Example
`examples/example-service.yaml` includes a small nginx app and an annotated LB Service.

## License
Apache-2.0
