package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"astuart.co/edgeos-rest/pkg/edgeos"
	"github.com/opentracing/opentracing-go"
	"github.com/sirupsen/logrus"
	glog "github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	annotPort    = "edgeos.microcumul.us/externalPort"
	annotProto   = "edgeos.microcumul.us/protocol" // tcp, udp, tcp_udp
	annotForward = "edgeos.microcumul.us/portForward"

	pfxK8sManaged = "kube-managed-pf-"

	maxAttempts = 5
)

var (
	endpointAttempts = map[string]int{}
)

func getExisting(ctx context.Context, edge *edgeos.Client) (map[string]edgeos.PortForward, error) {
	pfs, err := edge.PortForwards()
	if err != nil {
		return nil, err
	}

	exist := map[string]edgeos.PortForward{}

	for _, pf := range pfs.Feature.Data.Rules {
		logrus.WithField("description", pf.Description).Info("Found port forward")
		exist[pf.Description] = pf
	}

	return exist, nil
}

type fwd struct {
	svc corev1.Service
	fwd edgeos.PortForward
}

func first(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func getKubeState(ctx context.Context, kube *kubernetes.Clientset) (map[string]fwd, error) {
	sp, ctx := opentracing.StartSpanFromContext(ctx, "getKubeState")
	defer sp.Finish()
	// Get ports to expose
	expose := map[string]fwd{}

	svcs, err := kube.CoreV1().Services("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	for _, svc := range svcs.Items {
		if svc.Annotations[annotForward] != "true" || len(svc.Spec.Ports) < 1 {
			continue
		}

		if svc.Spec.Type == "LoadBalancer" {
			for _, p := range svc.Spec.Ports {
				port := fmt.Sprint(p.Port)
				newPF := edgeos.PortForward{
					PortFrom: port,
					PortTo:   port,
					IPTo:     svc.Status.LoadBalancer.Ingress[0].IP,
					Protocol: strings.ToLower(first(svc.Annotations[annotProto], string(p.Protocol))),

					Description: pfxK8sManaged + svc.Namespace + "/" + svc.Name + ":" + port,
				}

				expose[pfxK8sManaged+svc.Namespace+"/"+svc.Name+":"+port] = fwd{svc, newPF}
			}

			continue
		}

		svcPorts := map[int]corev1.ServicePort{}
		svcNames := map[string]corev1.ServicePort{}

		for _, sp := range svc.Spec.Ports {
			svcPorts[sp.TargetPort.IntValue()] = sp
			svcNames[sp.TargetPort.String()] = sp
		}

		ep, err := kube.CoreV1().Endpoints(svc.Namespace).Get(ctx, svc.Name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}

		for _, subset := range ep.Subsets {
			if len(subset.Addresses) < 1 || len(subset.Ports) < 1 {
				continue
			}
			addr := subset.Addresses[0]

			for _, p := range subset.Ports {

				port := fmt.Sprint(p.Port)

				// First check service ports by name
				ext, ok := svcNames[p.Name]
				if !ok || p.Name == "" {
					// Fall back to number if name not specified
					ext = svcPorts[int(p.Port)]
				}

				extPort := fmt.Sprint(ext.Port)

				newPF := edgeos.PortForward{
					PortFrom: extPort,
					PortTo:   port,
					IPTo:     addr.IP,
					Protocol: "tcp_udp",

					Description: pfxK8sManaged + svc.Namespace + "/" + svc.Name + ":" + port,
				}

				expose[pfxK8sManaged+svc.Namespace+"/"+svc.Name+":"+port] = fwd{svc, newPF}
			}
		}
	}

	return expose, nil
}

func (wa *watcher) sync() error {
	ctx := context.TODO()
	if err := wa.e.Login(); err != nil {
		return fmt.Errorf("Sync error -- could not login to edgeos router: %s", err)
	}

	exist, err := getExisting(ctx, wa.e)
	if err != nil {
		return err
	}

	expose, err := getKubeState(ctx, wa.k)
	if err != nil {
		return err
	}

	// Get ports already exposed

	del := []string{}

	// Remove from the "exist" map if services are not to be exposed.
	for k, pf := range exist {
		logrus.Infof("Description %s encountered", pf.Description)
		if strings.Index(pf.Description, pfxK8sManaged) != 0 {
			continue
		}
		// If an existing k8s-managed port forward is no longer needed, delete
		if _, ok := expose[k]; !ok {
			del = append(del, k)
		}

		// If we're already managing, don't re-expose
		if expose[pf.Description].fwd == pf {
			delete(expose, pf.Description)
		}
	}

	// Remove any we don't want to manage
	for _, d := range del {
		logrus.WithField("item", d).Infof("Removing configuration item")
		delete(exist, d)
	}

	// TODO check for conflicting port *and* namespace+svcName

	// Expose services to effectively create/update
	for desc, pf := range expose {
		glog.WithField("description", pf.fwd.Description).Info("Exposing service")
		exist[desc] = pf.fwd
	}

	pfs, err := wa.e.PortForwards()
	if err != nil {
		return err
	}

	pfs.Feature.Data.Rules = []edgeos.PortForward{}
	for _, pf := range exist {
		pfs.Feature.Data.Rules = append(pfs.Feature.Data.Rules, pf)
	}

	if len(del)+len(expose) == 0 {
		glog.Info("No updates to make; not persisting changes")
		return nil
	}

	glog.Infof("%d rules added, %d rules deleted, %d total", len(expose), len(del), len(pfs.Feature.Data.Rules))
	glog.Infof("Updating port forwarding to the following list of rules: %#v", pfs.Feature.Data.Rules)
	_, err = wa.e.SetFeature(edgeos.PortForwarding, pfs.Feature.Data)

	if err != nil {
		glog.WithError(err).Error("could not set features")
		return err
	}

svcup:
	for _, ex := range expose {
		gg := glog.WithField("ns", ex.svc.ObjectMeta.Namespace).WithField("service", ex.svc.ObjectMeta.Name)
		svc, err := wa.k.CoreV1().Services(ex.svc.ObjectMeta.Namespace).Get(ctx, ex.svc.ObjectMeta.Name, metav1.GetOptions{})
		if err != nil {
			gg.WithError(err).Error("could not get ns")
		}
		for _, ing := range svc.Status.LoadBalancer.Ingress {
			if ing.Hostname == "h.microcumul.us" {
				gg.Info("skipping")
				continue svcup
			}
		}
		svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{
			Hostname: "h.microcumul.us",
		}}

		gg.Info("updating status")
		_, err = wa.k.CoreV1().Services(svc.ObjectMeta.Namespace).UpdateStatus(ctx, svc, metav1.UpdateOptions{})
		if err != nil {
			gg.WithError(err).Error("could not update lb status of service")
		}
	}

	return nil
}

func (wa *watcher) watch() error {
	ch := make(chan *corev1.Service)

	ctx := context.TODO()

	go wa.watchEndpoints(ctx, ch)
	go wa.watchServices(ctx, ch)

	for {
		select {
		case <-ch:
			// TODO sync specific service
			err := wa.sync()
			if err != nil {
				glog.Errorf("Error syncing during watch: %s", err)
			}

		}
	}

}

type watcher struct {
	interval time.Duration
	k        *kubernetes.Clientset
	e        *edgeos.Client
}

func (wa *watcher) watchEndpoints(ctx context.Context, ch chan *corev1.Service) error {
	for {
		w, err := wa.k.CoreV1().Endpoints("").Watch(ctx, metav1.ListOptions{})
		if err != nil {
			return err
		}

		for ep := range w.ResultChan() {
			ep := ep.Object.(*corev1.Endpoints)

			key := fmt.Sprintf("%s/%s", ep.Namespace, ep.Name)
			attempts := endpointAttempts[key]
			if attempts > maxAttempts {
				continue
			}

			svc, err := wa.k.CoreV1().Services(ep.Namespace).Get(ctx, ep.Name, metav1.GetOptions{})

			if err != nil {
				attempts++
				endpointAttempts[key] = attempts
				glog.Errorf("Error getting service with name %q (attempt %d)", ep.Name, attempts)
				continue
			}

			if svc.Annotations[annotForward] == "true" {
				ch <- svc
			}
		}

		glog.Debugf("Sleeping %s", interval)
		time.Sleep(wa.interval)
	}
}

func (wa *watcher) watchServices(ctx context.Context, ch chan *corev1.Service) error {
	for {
		w, err := wa.k.CoreV1().Services("").Watch(ctx, metav1.ListOptions{})
		if err != nil {
			return err
		}

		for evt := range w.ResultChan() {
			svc := evt.Object.(*corev1.Service)

			if svc.Annotations[annotForward] == "true" {
				ch <- svc
			}
		}

		glog.Debugf("Sleeping %s", interval)
		time.Sleep(wa.interval)
	}
}
