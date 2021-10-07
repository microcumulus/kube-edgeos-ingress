REG=docker.io
IMAGE=andrewstuart/kube-edgeos-ingress

.PHONY: build push deploy

TAG=$(REG)/$(IMAGE)
SHA=$(shell docker inspect --format "{{ index .RepoDigests 0 }}" $(1))

build: 
	CGO_ENABLED=0 go build -o app

dockerize: build
	docker build -t $(TAG) .

push: dockerize
	docker push $(TAG)

deploy: push
	kubectl --namespace kube-system apply -f k8s.yaml
	kubectl --namespace kube-system set image deployments/edgeos-ingress edgeos-ingress=$(call SHA,$(TAG))
