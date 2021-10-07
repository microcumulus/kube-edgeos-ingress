package main

import (
	"os"

	"astuart.co/edgeos-rest/pkg/edgeos"
	log "github.com/sirupsen/logrus"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func mustGetEdge() *edgeos.Client {
	eCli, err := edgeos.NewClient(os.Getenv("ERLITE_ADDR"), os.Getenv("ERLITE_USER"), os.Getenv("ERLITE_PASS"))
	if err != nil {
		log.Fatal("Edgeos client error", err)
	}

	err = eCli.Login()
	if err != nil {
		log.Fatal("EdgeOS Login error", err)
	}

	return eCli
}

func mustGetKube(inCluster bool) *kubernetes.Clientset {
	var config *rest.Config
	var err error

	if inCluster {
		config, err = rest.InClusterConfig()
		if err != nil {
			log.Fatal(err)
		}
	} else {
		config = &rest.Config{
			Host: *host,
		}
	}

	kube, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatal(err)
	}

	return kube
}
