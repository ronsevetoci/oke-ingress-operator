// SPDX-License-Identifier: Apache-2.0
// oke-ingress-combined-operator
// - Controller A: EndpointSlice-driven node labeler for ingress Service
// - Controller B: CCM backend-set sync trigger for Services annotated with
//   oci.oraclecloud.com/node-label-selector
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	discoveryv1 "k8s.io/api/discovery/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

// ====== Shared setup ======

var scheme = clientgoscheme.Scheme

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(discoveryv1.AddToScheme(scheme))
}

func getenv(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

// ====== Controller A: Ingress EndpointSlice -> Node labeler ======

type LabelerConfig struct {
	IngressNS  string
	IngressSvc string
	LabelKey   string
	LabelVal   string
}

type SliceReconciler struct {
	client.Client
	cfg LabelerConfig
}

func (r *SliceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx).WithValues("endpointslice", req.NamespacedName)

	// List EndpointSlices for the target Service
	var slices discoveryv1.EndpointSliceList
	if err := r.List(ctx, &slices, &client.ListOptions{
		Namespace: r.cfg.IngressNS,
		LabelSelector: labels.SelectorFromSet(labels.Set{
			discoveryv1.LabelServiceName: r.cfg.IngressSvc,
		}),
	}); err != nil {
		return ctrl.Result{}, err
	}

	// Desired nodes: those with Ready endpoints for the Service
	desired := map[string]struct{}{}
	for _, es := range slices.Items {
		for _, ep := range es.Endpoints {
			if ep.Conditions.Ready != nil && *ep.Conditions.Ready {
				if ep.NodeName != nil && *ep.NodeName != "" {
					desired[*ep.NodeName] = struct{}{}
				}
			}
		}
	}
	log.Info("computed desired nodes from EndpointSlices",
		"service", fmt.Sprintf("%s/%s", r.cfg.IngressNS, r.cfg.IngressSvc),
		"desiredCount", len(desired))

	// Reconcile node labels
	var nodes corev1.NodeList
	if err := r.List(ctx, &nodes); err != nil {
		return ctrl.Result{}, err
	}
	for i := range nodes.Items {
		n := &nodes.Items[i]
		has := n.Labels[r.cfg.LabelKey] == r.cfg.LabelVal
		_, want := desired[n.Name]

		switch {
		case want && !has:
			patch := client.MergeFrom(n.DeepCopy())
			if n.Labels == nil {
				n.Labels = map[string]string{}
			}
			n.Labels[r.cfg.LabelKey] = r.cfg.LabelVal
			if err := r.Patch(ctx, n, patch); err != nil {
				log.Error(err, "failed to add label", "node", n.Name)
				return ctrl.Result{}, err
			}
			log.Info("labeled node", "node", n.Name, "label", r.cfg.LabelKey+"="+r.cfg.LabelVal)
		case !want && has:
			patch := client.MergeFrom(n.DeepCopy())
			delete(n.Labels, r.cfg.LabelKey)
			if err := r.Patch(ctx, n, patch); err != nil {
				log.Error(err, "failed to remove label", "node", n.Name)
				return ctrl.Result{}, err
			}
			log.Info("removed label", "node", n.Name, "label", r.cfg.LabelKey)
		}
	}

	log.Info("label sync complete", "labelKey", r.cfg.LabelKey, "labelValue", r.cfg.LabelVal)
	return ctrl.Result{}, nil
}

func (r *SliceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	pred := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool { return r.isIngressSlice(e.Object) },
		UpdateFunc: func(e event.UpdateEvent) bool { return r.isIngressSlice(e.ObjectNew) || r.isIngressSlice(e.ObjectOld) },
		DeleteFunc: func(e event.DeleteEvent) bool { return r.isIngressSlice(e.Object) },
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&discoveryv1.EndpointSlice{}, builder.WithPredicates(pred)).
		WithOptions(controller.Options{MaxConcurrentReconciles: 2}).
		Complete(r)
}

func (r *SliceReconciler) isIngressSlice(obj client.Object) bool {
	es, ok := obj.(*discoveryv1.EndpointSlice)
	if !ok {
		return false
	}
	if es.Namespace != r.cfg.IngressNS {
		return false
	}
	return es.Labels[discoveryv1.LabelServiceName] == r.cfg.IngressSvc
}

// ====== Controller B: Service -> poke CCM backend-set sync ======

const (
	AnnotationNodeSelector = "oci.oraclecloud.com/node-label-selector"
	AnnotationTrigger      = "oci.oraclecloud.com/trigger-reconcile"
)

type ServiceReconciler struct {
	client.Client
	Recorder record.EventRecorder
}

func (r *ServiceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx).WithValues("service", req.NamespacedName)

	var svc corev1.Service
	if err := r.Get(ctx, req.NamespacedName, &svc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if svc.Spec.Type != corev1.ServiceTypeLoadBalancer {
		return ctrl.Result{}, nil
	}
	selectorString, ok := svc.Annotations[AnnotationNodeSelector]
	if !ok || selectorString == "" {
		return ctrl.Result{}, nil
	}
	selector, err := labels.Parse(selectorString)
	if err != nil {
		r.Recorder.Eventf(&svc, corev1.EventTypeWarning, "InvalidSelector", "failed to parse %s: %v", AnnotationNodeSelector, err)
		log.Error(err, "invalid label selector", "selector", selectorString)
		return ctrl.Result{}, nil
	}
	log.Info("reconciling LB service with node-label selector",
		"namespace", svc.Namespace, "name", svc.Name,
		"selector", selectorString,
		"externalTrafficPolicy", string(svc.Spec.ExternalTrafficPolicy))

	var nodeList corev1.NodeList
	if err := r.List(ctx, &nodeList, &client.ListOptions{LabelSelector: selector}); err != nil {
		return ctrl.Result{}, err
	}

	candidateIPs := map[string]struct{}{}
	for _, n := range nodeList.Items {
		if isNodeReady(&n) && isNodeSchedulable(&n) {
			ip := nodePrimaryInternalIP(&n)
			if ip != "" {
				candidateIPs[ip] = struct{}{}
			}
		}
	}
	log.Info("candidate nodes (pre-ETP filter)", "count", len(candidateIPs))

	// When ETP=Local, restrict to nodes that actually host endpoints
	if svc.Spec.ExternalTrafficPolicy == corev1.ServiceExternalTrafficPolicyTypeLocal {
		var slices discoveryv1.EndpointSliceList
		if err := r.List(ctx, &slices, &client.ListOptions{
			Namespace: svc.Namespace,
			LabelSelector: labels.SelectorFromSet(labels.Set{
				discoveryv1.LabelServiceName: svc.Name,
			}),
		}); err != nil {
			return ctrl.Result{}, err
		}
		nodesWithEP := map[string]struct{}{}
		for _, es := range slices.Items {
			for _, ep := range es.Endpoints {
				if ep.NodeName != nil {
					nodesWithEP[*ep.NodeName] = struct{}{}
				}
			}
		}
		filtered := map[string]struct{}{}
		for _, n := range nodeList.Items {
			if _, ok := nodesWithEP[n.Name]; ok && isNodeReady(&n) && isNodeSchedulable(&n) && selector.Matches(labels.Set(n.Labels)) {
				ip := nodePrimaryInternalIP(&n)
				if ip != "" {
					filtered[ip] = struct{}{}
				}
			}
		}
		candidateIPs = filtered
		log.Info("candidate nodes (post-ETP filter)", "count", len(candidateIPs))
	}

	// Poke CCM by touching an annotation
	patch := client.MergeFrom(svc.DeepCopy())
	if svc.Annotations == nil {
		svc.Annotations = map[string]string{}
	}
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	svc.Annotations[AnnotationTrigger] = ts
	if err := r.Patch(ctx, &svc, patch); err != nil {
		return ctrl.Result{}, err
	}
	log.Info("patched service to trigger CCM",
		"annotation", AnnotationTrigger,
		"timestamp", svc.Annotations[AnnotationTrigger])

	// Emit an informative event
	if len(candidateIPs) > 0 {
		i := 0
		acc := make([]string, 0, 5)
		for ip := range candidateIPs {
			acc = append(acc, ip)
			i++
			if i == 5 {
				break
			}
		}
		r.Recorder.Eventf(&svc, corev1.EventTypeNormal, "TriggerCCM",
			"Triggered CCM refresh for %d candidate backend nodes (e.g., %v)", len(candidateIPs), acc)
	} else {
		r.Recorder.Eventf(&svc, corev1.EventTypeNormal, "TriggerCCM",
			"Triggered CCM refresh; zero matching backend nodes after filters")
	}

	return ctrl.Result{}, nil
}

func (r *ServiceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Watch Services with the special annotation; also react to Node and EndpointSlice changes.
	svcPred := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool { return isLBWithSelector(e.Object) },
		UpdateFunc: func(e event.UpdateEvent) bool { return isLBWithSelector(e.ObjectNew) },
		DeleteFunc: func(e event.DeleteEvent) bool { return false },
	}
	nodePred := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool { return true },
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldN := e.ObjectOld.(*corev1.Node)
			newN := e.ObjectNew.(*corev1.Node)
			if isNodeReady(oldN) != isNodeReady(newN) || isNodeSchedulable(oldN) != isNodeSchedulable(newN) {
				return true
			}
			return !labels.Equals(labels.Set(oldN.Labels), labels.Set(newN.Labels))
		},
		DeleteFunc: func(e event.DeleteEvent) bool { return true },
	}

	nodeToSvc := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		var list corev1.ServiceList
		if err := r.List(ctx, &list); err != nil {
			return nil
		}
		out := make([]reconcile.Request, 0, len(list.Items))
		for _, s := range list.Items {
			if isLBWithSelector(&s) {
				out = append(out, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: s.Namespace, Name: s.Name}})
			}
		}
		return out
	})

	esToSvc := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		es := obj.(*discoveryv1.EndpointSlice)
		svcName := es.Labels[discoveryv1.LabelServiceName]
		if svcName == "" {
			return nil
		}
		var svc corev1.Service
		if err := r.Get(ctx, types.NamespacedName{Namespace: es.Namespace, Name: svcName}, &svc); err != nil {
			return nil
		}
		if !isLBWithSelector(&svc) {
			return nil
		}
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: svc.Namespace, Name: svc.Name}}}
	})

	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Service{}, builder.WithPredicates(svcPred)).
		Watches(&corev1.Node{}, nodeToSvc, builder.WithPredicates(nodePred)).
		Watches(&discoveryv1.EndpointSlice{}, esToSvc).
		WithOptions(controller.Options{MaxConcurrentReconciles: 2}).
		Complete(r)
}

func isLBWithSelector(obj client.Object) bool {
	svc, ok := obj.(*corev1.Service)
	if !ok {
		return false
	}
	if svc.Spec.Type != corev1.ServiceTypeLoadBalancer {
		return false
	}
	_, ok = svc.Annotations[AnnotationNodeSelector]
	return ok
}

func isNodeReady(n *corev1.Node) bool {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func isNodeSchedulable(n *corev1.Node) bool { return !n.Spec.Unschedulable }

func nodePrimaryInternalIP(n *corev1.Node) string {
	for _, a := range n.Status.Addresses {
		if a.Type == corev1.NodeInternalIP {
			return a.Address
		}
	}
	return ""
}

// ====== main: flags & manager ======

func main() {
	var metricsAddr, probeAddr string
	var leader bool
	var enableLabeler, enableSync bool

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "metrics address")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "probe address")
	flag.BoolVar(&leader, "leader-elect", true, "leader election")
	flag.BoolVar(&enableLabeler, "enable-labeler", true, "enable EndpointSlice-driven node labeler")
	flag.BoolVar(&enableSync, "enable-backend-sync", true, "enable CCM backend-set poke for annotated Services")
	flag.Parse()

	// Structured logger (Zap) and flags
	opts := zap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         leader,
		LeaderElectionID:       "oke-ingress-combined-operator",
	})
	if err != nil {
		panic(fmt.Errorf("manager start: %w", err))
	}
	setupLog := ctrl.Log.WithName("setup")
	setupLog.Info("manager started", "leaderElection", leader)

	// Controller A
	if enableLabeler {
		lcfg := LabelerConfig{
			IngressNS:  getenv("INGRESS_NAMESPACE", "ingress-nginx"),
			IngressSvc: getenv("INGRESS_SERVICE", "ingress-nginx-controller"),
			LabelKey:   getenv("LABEL_KEY", "role"),
			LabelVal:   getenv("LABEL_VALUE", "ingress"),
		}
		ctrl.Log.WithName("labeler").Info("enabled",
			"ingressNamespace", lcfg.IngressNS,
			"ingressService", lcfg.IngressSvc,
			"labelKey", lcfg.LabelKey, "labelValue", lcfg.LabelVal)

		if err := (&SliceReconciler{Client: mgr.GetClient(), cfg: lcfg}).SetupWithManager(mgr); err != nil {
			panic(fmt.Errorf("setup labeler controller: %w", err))
		}
	}

	// Controller B
	if enableSync {
		reconciler := &ServiceReconciler{
			Client:   mgr.GetClient(),
			Recorder: mgr.GetEventRecorderFor("oke-ingress-combined-operator"),
		}
		ctrl.Log.WithName("backend-sync").Info("enabled", "annotation", AnnotationNodeSelector)

		if err := reconciler.SetupWithManager(mgr); err != nil {
			panic(fmt.Errorf("setup backend-sync controller: %w", err))
		}
	}

	_ = mgr.AddHealthzCheck("healthz", healthz.Ping)
	_ = mgr.AddReadyzCheck("readyz", healthz.Ping)

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		panic(fmt.Errorf("run manager: %w", err))
	}
}
