package resourcewatcher

import (
	"context"
	"time"

	"github.com/bonnefoa/kubectl-fzf/pkg/k8sresources"
	"github.com/bonnefoa/kubectl-fzf/pkg/util"
	"github.com/golang/glog"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	_ "k8s.io/client-go/plugin/pkg/client/auth/oidc"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/kubernetes/staging/src/k8s.io/client-go/tools/cache"
)

type resourceWatcher struct {
	clientset *kubernetes.Clientset
	namespace string
	cluster   string
}

type watchConfig struct {
	resourceCtor  func(obj interface{}) k8sresources.K8sResource
	header        string
	resourceName  string
	getter        cache.Getter
	runtimeObject runtime.Object
	hasNamespace  bool
	pollingPeriod time.Duration
}

func NewResourceWatcher(namespace string, kubeconfig string) resourceWatcher {
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	util.FatalIf(err)

	resourceWatcher := resourceWatcher{}
	resourceWatcher.namespace = namespace
	resourceWatcher.clientset, err = kubernetes.NewForConfig(config)

	util.FatalIf(err)
	return resourceWatcher
}

func (r *resourceWatcher) Start(ctx context.Context, cfg watchConfig, cacheDir string) error {
	store, err := NewK8sStore(cfg, cacheDir)
	if err != nil {
		return err
	}
	if cfg.pollingPeriod > 0 {
		go r.pollResource(ctx, cfg, store)
	} else {
		go r.watchResource(ctx, cfg, store)
	}
	return nil
}

func (r *resourceWatcher) GetWatchConfigs(nodePollingPeriod time.Duration, namespacePollingPeriod time.Duration) []watchConfig {
	coreGetter := r.clientset.Core().RESTClient()
	appsGetter := r.clientset.Apps().RESTClient()

	watchConfigs := []watchConfig{
		watchConfig{k8sresources.NewPodFromRuntime, k8sresources.PodHeader, string(corev1.ResourcePods), coreGetter, &corev1.Pod{}, true, 0},
		watchConfig{k8sresources.NewServiceFromRuntime, k8sresources.ServiceHeader, string(corev1.ResourceServices), coreGetter, &corev1.Service{}, true, 0},
		watchConfig{k8sresources.NewReplicaSetFromRuntime, k8sresources.ReplicaSetHeader, "replicasets", appsGetter, &appsv1.ReplicaSet{}, true, 0},
		watchConfig{k8sresources.NewConfigMapFromRuntime, k8sresources.ConfigMapHeader, "configmaps", coreGetter, &corev1.ConfigMap{}, true, 0},
		watchConfig{k8sresources.NewStatefulSetFromRuntime, k8sresources.StatefulSetHeader, "statefulsets", appsGetter, &appsv1.StatefulSet{}, true, 0},
		watchConfig{k8sresources.NewDeploymentFromRuntime, k8sresources.DeploymentHeader, "deployments", appsGetter, &appsv1.Deployment{}, true, 0},
		watchConfig{k8sresources.NewEndpointsFromRuntime, k8sresources.EndpointsHeader, "endpoints", coreGetter, &corev1.Endpoints{}, true, 0},
		watchConfig{k8sresources.NewPersistentVolumeFromRuntime, k8sresources.PersistentVolumeHeader, "persistentvolumes", coreGetter, &corev1.PersistentVolume{}, false, 0},
		watchConfig{k8sresources.NewPersistentVolumeClaimFromRuntime, k8sresources.PersistentVolumeClaimHeader, string(corev1.ResourcePersistentVolumeClaims), coreGetter, &corev1.PersistentVolumeClaim{}, true, 0},
		watchConfig{k8sresources.NewNodeFromRuntime, k8sresources.NodeHeader, "nodes", coreGetter, &corev1.Node{}, false, nodePollingPeriod},
		watchConfig{k8sresources.NewNamespaceFromRuntime, k8sresources.NamespaceHeader, "namespaces", coreGetter, &corev1.Namespace{}, false, namespacePollingPeriod},
	}
	return watchConfigs
}

func (r *resourceWatcher) pollResource(ctx context.Context,
	cfg watchConfig, k8sStore K8sStore) {
	glog.V(4).Infof("Start poller for %s on namespace %s", k8sStore.resourceName, r.namespace)
	namespace := ""
	if cfg.hasNamespace {
		namespace = r.namespace
	}
	watchlist := cache.NewListWatchFromClient(cfg.getter,
		k8sStore.resourceName, namespace, fields.Everything())
	for {
		obj, err := watchlist.List(metav1.ListOptions{})
		if err != nil {
			glog.Warningf("Error on listing %s: %v", k8sStore.resourceName, err)
		}
		lst, err := apimeta.ExtractList(obj)
		if err != nil {
			glog.Warningf("Error extracting list %s: %v", k8sStore.resourceName, err)
		}
		k8sStore.AddResourceList(lst)
		time.Sleep(cfg.pollingPeriod)
	}
}

func (r *resourceWatcher) watchResource(ctx context.Context,
	cfg watchConfig, k8sStore K8sStore) {
	glog.V(4).Infof("Start watch for %s on namespace %s", k8sStore.resourceName, r.namespace)
	namespace := ""
	if cfg.hasNamespace {
		namespace = r.namespace
	}
	watchlist := cache.NewListWatchFromClient(cfg.getter,
		k8sStore.resourceName, namespace, fields.Everything())

	_, controller := cache.NewInformer(
		watchlist, cfg.runtimeObject, time.Second*0,
		cache.ResourceEventHandlerFuncs{
			AddFunc:    k8sStore.AddResource,
			DeleteFunc: k8sStore.DeleteResource,
			UpdateFunc: k8sStore.UpdateResource,
		},
	)

	stop := make(chan struct{})
	go controller.Run(stop)
	<-ctx.Done()
	close(stop)
}