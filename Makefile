SUBDIR :=

DOCKER_IMAGE ?= imagelet:dev

.PHONY: all clean test run build upgrade docker-build docker-run help $(SUBDIR)

all: $(SUBDIR) 		# default action
	@[ -f .git/hooks/pre-commit ] || pre-commit install --install-hooks
	@git config commit.template .git-commit-template

clean: $(SUBDIR)	# clean-up environment
	@find . -name '*.sw[po]' -delete
	@rm -rf bin/

test:				# run test
	go test ./... -count=1

run:				# run in the local environment
	go run ./cmd/imagelet

build:				# build the binary/library
	go build -o bin/imagelet ./cmd/imagelet

docker-build:		# build the Docker image (override with DOCKER_IMAGE=...)
	docker build -t $(DOCKER_IMAGE) .

docker-run:			# run the Docker image, mapping host port 8080
	docker run --rm -p 8080:8080 $(DOCKER_IMAGE)

upgrade:			# upgrade all the necessary packages
	pre-commit autoupdate

help:				# show this message
	@printf "Usage: make [OPTION]\n"
	@printf "\n"
	@perl -nle 'print $$& if m{^[\w-]+:.*?#.*$$}' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?#"} {printf "    %-18s %s\n", $$1, $$2}'

$(SUBDIR):
	$(MAKE) -C $@ $(MAKECMDGOALS)
