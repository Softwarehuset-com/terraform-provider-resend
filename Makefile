 default: build

build:
	go build -o terraform-provider-resend .

fmt:
	gofmt -w .

fmt-check:
	@output=$$(gofmt -l .); if [ -n "$$output" ]; then echo $$output; exit 1; fi

test: fmt-check
	go test ./...

testacc:
	TF_ACC=1 go test ./... -run TestAcc -v $(TESTARGS)

vet:
	go vet ./...

docs:
	go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs@latest generate --provider-name resend

.PHONY: default build fmt fmt-check test testacc vet docs
