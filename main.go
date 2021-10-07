package main

import (
	"flag"
	"time"

	"github.com/sirupsen/logrus"
)

var (
	inCluster = flag.Bool("in-cluster", false, "the client is running in-cluster")
	host      = flag.String("host", "localhost:8001", "if not in-cluster, the host the client will communicate with")
	interval  = flag.Duration("interval", time.Second, "the sync interval for the client to run")
)

func init() {
	flag.Parse()
}

func main() {
	e := mustGetEdge()
	k := mustGetKube(*inCluster)

	wa := watcher{
		e:        e,
		k:        k,
		interval: *interval,
	}

	if err := wa.sync(); err != nil {
		logrus.Error(err)
	}

	if err := wa.watch(); err != nil {
		logrus.Fatalf("Error watching: %s", err)
	}
}
