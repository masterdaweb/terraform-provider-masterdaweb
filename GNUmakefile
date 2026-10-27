TFPLUGINDOCS = go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs@v0.25.0

default: test

build:
	go build ./...

install:
	go install .

test:
	go test ./...

vet:
	go vet ./...

# The complete example and the updates, through Terraform itself, against
# the fake API (downloads Terraform when none is installed).
e2e:
	MW_TF_E2E=1 go test ./internal/provider -run ThroughTerraform -v

# Real resources on the real API (MW_TOKEN): billed as any others.
testacc:
	TF_ACC=1 go test ./internal/provider -run TestAcc -v -timeout 60m

docs:
	$(TFPLUGINDOCS) generate --provider-name masterdaweb

docs-validate:
	$(TFPLUGINDOCS) validate --provider-name masterdaweb

.PHONY: default build install test vet e2e testacc docs docs-validate
