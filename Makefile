# SPDX-License-Identifier: GPL-3.0-or-later

CONTROLLER_GEN ?= $(HOME)/go/bin/controller-gen
IMG ?= fusion-weave-operator:latest
NAMESPACE ?= fusion

.PHONY: all build test generate manifests docker-build deploy undeploy install-crds

all: generate build

## Generate deepcopy methods and CRD manifests.
generate:
	$(CONTROLLER_GEN) object:headerFile="" paths="./api/..."
	$(CONTROLLER_GEN) crd paths="./api/..." output:crd:dir=config/crd/bases

## Build the operator binary.
build: generate
	CGO_ENABLED=0 go build -o bin/manager ./cmd/

## Run unit tests.
test:
	go test ./... -v

## Build the Docker image and load it directly into minikube.
docker-build:
	eval $$(minikube docker-env) && docker build -t $(IMG) .

## Create the fusion namespace (idempotent).
create-namespace:
	kubectl create namespace $(NAMESPACE) --dry-run=client -o yaml | kubectl apply -f -

## Install CRDs into the cluster.
install-crds:
	kubectl apply -f config/crd/bases/

## Apply RBAC resources.
install-rbac:
	kubectl apply -f config/rbac/

## Deploy the operator.
deploy: install-crds install-rbac
	kubectl apply -f config/manager/manager.yaml

## Remove the operator deployment.
undeploy:
	kubectl delete -f config/manager/manager.yaml --ignore-not-found
	kubectl delete -f config/rbac/ --ignore-not-found
	kubectl delete -f config/crd/bases/ --ignore-not-found

## Apply sample resources.
samples:
	kubectl apply -f config/samples/weavejobtemplate_echo.yaml
	kubectl apply -f config/samples/weavechain_pipeline.yaml
	kubectl apply -f config/samples/weavetrigger_ondemand.yaml

## Full minikube deploy cycle: build image, install CRDs+RBAC, deploy operator.
minikube-deploy: create-namespace docker-build deploy
