BINARY  := podcast-agent
CMD     := ./cmd/$(BINARY)
BIN_DIR := bin
IMAGE   := $(BINARY)
TAG     ?= latest
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
# Must match the EKS nodes (node_arch in Terraform; arm64 by default).
PLATFORM ?= linux/arm64

TF        := terraform -chdir=deploy/terraform
CHART     := deploy/helm/podcast-agent
NAMESPACE := podcast-agent

.PHONY: all build test run docker push clean deploy destroy

all: build

build:
	go build -o $(BIN_DIR)/$(BINARY) $(CMD)

test:
	go test ./...

run:
	go run $(CMD)

docker:
	docker build --platform $(PLATFORM) --build-arg VERSION=$(VERSION) -t $(IMAGE):$(TAG) .

# Push the image built by `make docker` to the ECR repo Terraform created.
push:
	$(eval REPO := $(shell $(TF) output -raw ecr_repository_url))
	aws ecr get-login-password --region $$($(TF) output -raw region) | docker login --username AWS --password-stdin $(firstword $(subst /, ,$(REPO)))
	docker tag $(IMAGE):$(TAG) $(REPO):$(TAG)
	docker push $(REPO):$(TAG)

clean:
	rm -rf $(BIN_DIR)

# Install or upgrade the worker on the cluster Terraform created, wiring in
# its outputs. Run `terraform apply` in deploy/terraform first.
deploy:
	aws eks update-kubeconfig --region $$($(TF) output -raw region) --name $$($(TF) output -raw cluster_name)
	helm upgrade --install $(BINARY) $(CHART) \
		--namespace $(NAMESPACE) --create-namespace \
		-f $(CHART)/values-demo.yaml \
		--set image.repository=$$($(TF) output -raw ecr_repository_url) \
		--set image.tag=$(TAG) \
		--set aws.region=$$($(TF) output -raw region) \
		--set aws.bucket=$$($(TF) output -raw bucket) \
		--set aws.queueURL=$$($(TF) output -raw queue_url)

# Remove the release before the cluster, so nothing is left running in it.
destroy:
	-helm uninstall $(BINARY) --namespace $(NAMESPACE)
	$(TF) destroy
