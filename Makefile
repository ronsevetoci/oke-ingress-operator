IMG ?= ocir.eu-frankfurt-1.oci.oraclecloud.com/frsxwtjslf35/oke-ingress-operator:0.3.0

.PHONY: build docker-build docker-push deploy helm-install

build:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/manager ./

docker-build:
	podman build --platform=linux/amd64 -t $(IMG) .

docker-push:
	podman push $(IMG)

deploy:
	kubectl apply -k config/deploy/

helm-install:
	helm upgrade --install oke-ingress-combined charts/oke-ingress-operator -n kube-system --create-namespace \
	  --set image.repository=$(word 1,$(subst :, ,$(IMG))) \
	  --set image.tag=$(word 2,$(subst :, ,$(IMG)))
