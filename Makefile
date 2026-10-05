BINARY  := podcast-agent
CMD     := ./cmd/$(BINARY)
BIN_DIR := bin
IMAGE   := $(BINARY)
TAG     ?= latest
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
# Must match the EKS nodes (node_arch in Terraform; arm64 by default).
PLATFORM ?= linux/arm64

TF        := terraform -chdir=deploy/terraform
COMPOSE   := docker compose --profile aws-local
AWSLOCAL  := $(COMPOSE) exec -T localstack awslocal
CHART     := deploy/helm/podcast-agent
NAMESPACE := podcast-agent

.PHONY: all build test docker push clean deploy destroy local-up local-down local-logs demo-local

all: build

build:
	go build -o $(BIN_DIR)/$(BINARY) $(CMD)

test:
	go test ./...

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

# --- LocalStack: the worker against local S3 + SQS (see docker-compose.yml) ---

local-up:
	$(COMPOSE) up -d --build --wait

local-down:
	$(COMPOSE) down -v

local-logs:
	$(COMPOSE) logs -f worker

# Upload a sample to incoming/ as a client would, wait for the worker to
# write its report, and print it. Episode IDs are the sample's name prefix.
SAMPLE  ?= ep001_remote_work.json
EPISODE ?= $(firstword $(subst _, ,$(SAMPLE)))
demo-local:
	$(AWSLOCAL) s3 cp /samples/$(SAMPLE) s3://podcasts/incoming/$(SAMPLE)
	@echo "Waiting for results/$(EPISODE)/ (make local-logs to follow the worker)..."
	@for i in $$(seq 120); do \
		$(AWSLOCAL) s3api head-object --bucket podcasts --key results/$(EPISODE)/source.json >/dev/null 2>&1 && break; \
		sleep 5; \
	done
	$(AWSLOCAL) s3 ls s3://podcasts/results/$(EPISODE)/
	$(AWSLOCAL) s3 cp s3://podcasts/results/$(EPISODE)/report.md -
