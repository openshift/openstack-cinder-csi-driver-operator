package operator

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apiextclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	corev1 "k8s.io/client-go/informers/core/v1"
	kubeclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	opv1 "github.com/openshift/api/operator/v1"
	configclient "github.com/openshift/client-go/config/clientset/versioned"
	configinformers "github.com/openshift/client-go/config/informers/externalversions"
	opclient "github.com/openshift/client-go/operator/clientset/versioned"
	opinformers "github.com/openshift/client-go/operator/informers/externalversions"
	"github.com/openshift/library-go/pkg/controller/controllercmd"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/csi/csicontrollerset"
	"github.com/openshift/library-go/pkg/operator/csi/csidrivercontrollerservicecontroller"
	"github.com/openshift/library-go/pkg/operator/csi/csidrivernodeservicecontroller"
	dc "github.com/openshift/library-go/pkg/operator/deploymentcontroller"
	goc "github.com/openshift/library-go/pkg/operator/genericoperatorclient"
	"github.com/openshift/library-go/pkg/operator/resource/resourcehash"
	"github.com/openshift/library-go/pkg/operator/v1helpers"

	"github.com/openshift/openstack-cinder-csi-driver-operator/assets"
	"github.com/openshift/openstack-cinder-csi-driver-operator/pkg/controllers/config"
	"github.com/openshift/openstack-cinder-csi-driver-operator/pkg/util"
)

const (
	operatorName          = "openstack-cinder-csi-driver-operator"
	operandName           = "openstack-cinder-csi-driver"
	instanceName          = "cinder.csi.openstack.org"
	cloudCredSecretName   = "openstack-cloud-credentials"
	metricsCertSecretName = "openstack-cinder-csi-driver-controller-metrics-serving-cert"
	trustedCAConfigMap    = "openstack-cinder-csi-driver-trusted-ca-bundle"

	resyncInterval = 20 * time.Minute
)

func addDCObjectHash(deployment *appsv1.Deployment, inputHashes map[string]string) error {
	if deployment == nil {
		return fmt.Errorf("invalid deployment: %v", deployment)
	}
	if deployment.Annotations == nil {
		deployment.Annotations = map[string]string{}
	}
	if deployment.Spec.Template.Annotations == nil {
		deployment.Spec.Template.Annotations = map[string]string{}
	}
	for k, v := range inputHashes {
		annotationKey := fmt.Sprintf("operator.openshift.io/dep-%s", k)
		if len(annotationKey) > 63 {
			hash := sha256.Sum256([]byte(k))
			annotationKey = fmt.Sprintf("operator.openshift.io/dep-%x", hash)
			annotationKey = annotationKey[:63]
		}
		deployment.Annotations[annotationKey] = v
		deployment.Spec.Template.Annotations[annotationKey] = v
	}
	return nil
}

// TODO(stephenfin): This should be migrated to library-go
// WithDCConfigMapHashAnnotationHook creates a deployment hook that annotates a Deployment with a config map's hash.
func WithDCConfigMapHashAnnotationHook(
	namespace string,
	configMapName string,
	configMapInformer corev1.ConfigMapInformer,
) dc.DeploymentHookFunc {
	return func(opSpec *opv1.OperatorSpec, deployment *appsv1.Deployment) error {
		inputHashes, err := resourcehash.MultipleObjectHashStringMapForObjectReferenceFromLister(
			configMapInformer.Lister(),
			nil,
			resourcehash.NewObjectRef().ForConfigMap().InNamespace(namespace).Named(configMapName),
		)
		if err != nil {
			return fmt.Errorf("invalid dependency reference: %w", err)
		}
		return addDCObjectHash(deployment, inputHashes)
	}
}

func addDSObjectHash(daemonSet *appsv1.DaemonSet, inputHashes map[string]string) error {
	if daemonSet == nil {
		return fmt.Errorf("invalid daemonSet: %v", daemonSet)
	}
	if daemonSet.Annotations == nil {
		daemonSet.Annotations = map[string]string{}
	}
	if daemonSet.Spec.Template.Annotations == nil {
		daemonSet.Spec.Template.Annotations = map[string]string{}
	}
	for k, v := range inputHashes {
		annotationKey := fmt.Sprintf("operator.openshift.io/dep-%s", k)
		if len(annotationKey) > 63 {
			hash := sha256.Sum256([]byte(k))
			annotationKey = fmt.Sprintf("operator.openshift.io/dep-%x", hash)
			annotationKey = annotationKey[:63]
		}
		daemonSet.Annotations[annotationKey] = v
		daemonSet.Spec.Template.Annotations[annotationKey] = v
	}
	return nil
}

// TODO(stephenfin): This should be migrated to library-go
// WithDSConfigMapHashAnnotationHook creates a DaemonSet hook that annotates a DaemonSet with a config map's hash.
func WithDSConfigMapHashAnnotationHook(
	namespace string,
	configMapName string,
	configMapInformer corev1.ConfigMapInformer,
) csidrivernodeservicecontroller.DaemonSetHookFunc {
	return func(_ *opv1.OperatorSpec, ds *appsv1.DaemonSet) error {
		inputHashes, err := resourcehash.MultipleObjectHashStringMapForObjectReferenceFromLister(
			configMapInformer.Lister(),
			nil,
			resourcehash.NewObjectRef().ForConfigMap().InNamespace(namespace).Named(configMapName),
		)
		if err != nil {
			return fmt.Errorf("invalid dependency reference: %w", err)
		}

		return addDSObjectHash(ds, inputHashes)
	}
}

func RunOperator(ctx context.Context, controllerConfig *controllercmd.ControllerContext) error {

	// Create clientsets and informers
	kubeClient := kubeclient.NewForConfigOrDie(rest.AddUserAgent(controllerConfig.KubeConfig, operatorName))
	kubeInformersForNamespaces := v1helpers.NewKubeInformersForNamespaces(kubeClient, util.DefaultNamespace, util.OpenShiftConfigNamespace, "")
	secretInformer := kubeInformersForNamespaces.InformersFor(util.DefaultNamespace).Core().V1().Secrets()
	configMapInformer := kubeInformersForNamespaces.InformersFor(util.DefaultNamespace).Core().V1().ConfigMaps()
	nodeInformer := kubeInformersForNamespaces.InformersFor("").Core().V1().Nodes()

	// Create apiextension client. This is used to verify is a VolumeSnapshotClass CRD exists.
	apiExtClient, err := apiextclient.NewForConfig(rest.AddUserAgent(controllerConfig.KubeConfig, operatorName))
	if err != nil {
		return err
	}

	// Create config clientset and informer. This is used to get the cluster ID
	configClient := configclient.NewForConfigOrDie(rest.AddUserAgent(controllerConfig.KubeConfig, operatorName))
	configInformers := configinformers.NewSharedInformerFactory(configClient, resyncInterval)

	// operator.openshift.io client, used for ClusterCSIDriver
	operatorClientSet := opclient.NewForConfigOrDie(rest.AddUserAgent(controllerConfig.KubeConfig, operatorName))
	operatorInformers := opinformers.NewSharedInformerFactory(operatorClientSet, resyncInterval)

	// Create GenericOperatorclient. This is used by the library-go controllers created down below
	gvr := opv1.SchemeGroupVersion.WithResource("clustercsidrivers")
	operatorClient, dynamicInformers, err := goc.NewClusterScopedOperatorClientWithConfigName(controllerConfig.KubeConfig, gvr, instanceName)
	if err != nil {
		return err
	}

	dynamicClient, err := dynamic.NewForConfig(controllerConfig.KubeConfig)
	if err != nil {
		return err
	}

	csiControllerSet := csicontrollerset.NewCSIControllerSet(
		operatorClient,
		controllerConfig.EventRecorder,
	).WithLogLevelController().WithManagementStateController(
		operandName,
		false,
	).WithStaticResourcesController(
		"OpenStackCinderDriverStaticResourcesController",
		kubeClient,
		dynamicClient,
		kubeInformersForNamespaces,
		assets.ReadFile,
		[]string{
			// Create RBAC before creating Service Accounts.
			// This prevents a race where the controller/node can
			// try to create pods before the RBAC has been loaded,
			// leading to an initial admission failure. We avoid
			// this by exploiting the fact that the pods cannot be
			// scheduled until the SA has been created.
			"rbac/main_attacher_binding.yaml",
			"rbac/privileged_role.yaml",
			"rbac/controller_privileged_binding.yaml",
			"rbac/node_privileged_binding.yaml",
			"rbac/main_provisioner_binding.yaml",
			"rbac/volumesnapshot_reader_provisioner_binding.yaml",
			"rbac/main_resizer_binding.yaml",
			"rbac/storageclass_reader_resizer_binding.yaml",
			"rbac/main_snapshotter_binding.yaml",
			"rbac/kube_rbac_proxy_role.yaml",
			"rbac/kube_rbac_proxy_binding.yaml",
			"rbac/prometheus_role.yaml",
			"rbac/prometheus_rolebinding.yaml",
			"rbac/lease_leader_election_role.yaml",
			"rbac/lease_leader_election_rolebinding.yaml",
			"csidriver.yaml",
			"controller_sa.yaml",
			"controller_pdb.yaml",
			"node_sa.yaml",
			"service.yaml",
			"cabundle_cm.yaml",
		},
	).WithConditionalStaticResourcesController(
		"OpenStackCinderDriverConditionalStaticResourcesController",
		kubeClient,
		dynamicClient,
		kubeInformersForNamespaces,
		assets.ReadFile,
		[]string{
			"volumesnapshotclass.yaml",
		},
		// Only install when CRD exists.
		func() bool {
			name := "volumesnapshotclasses.snapshot.storage.k8s.io"
			_, err := apiExtClient.ApiextensionsV1().CustomResourceDefinitions().Get(context.TODO(), name, metav1.GetOptions{})
			return err == nil
		},
		// Don't ever remove.
		func() bool {
			return false
		},
	).WithCSIConfigObserverController(
		"OpenStackCinderDriverCSIConfigObserverController",
		configInformers,
	).WithCSIDriverControllerService(
		"OpenStackCinderDriverControllerServiceController",
		assets.ReadFile,
		"controller.yaml",
		kubeClient,
		kubeInformersForNamespaces.InformersFor(util.DefaultNamespace),
		configInformers,
		[]factory.Informer{
			nodeInformer.Informer(),
			secretInformer.Informer(),
			configMapInformer.Informer(),
			configInformers.Config().V1().Proxies().Informer(),
		},
		csidrivercontrollerservicecontroller.WithSecretHashAnnotationHook(util.DefaultNamespace, cloudCredSecretName, secretInformer),
		csidrivercontrollerservicecontroller.WithSecretHashAnnotationHook(util.DefaultNamespace, metricsCertSecretName, secretInformer),
		WithDCConfigMapHashAnnotationHook(util.DefaultNamespace, util.CinderConfigName, configMapInformer),
		csidrivercontrollerservicecontroller.WithObservedProxyDeploymentHook(),
		csidrivercontrollerservicecontroller.WithCABundleDeploymentHook(
			util.DefaultNamespace,
			trustedCAConfigMap,
			configMapInformer,
		),
		csidrivercontrollerservicecontroller.WithReplicasHook(nodeInformer.Lister()),
	).WithCSIDriverNodeService(
		"OpenStackCinderDriverNodeServiceController",
		assets.ReadFile,
		"node.yaml",
		kubeClient,
		kubeInformersForNamespaces.InformersFor(util.DefaultNamespace),
		[]factory.Informer{configMapInformer.Informer()},
		csidrivernodeservicecontroller.WithSecretHashAnnotationHook(util.DefaultNamespace, cloudCredSecretName, secretInformer),
		csidrivernodeservicecontroller.WithObservedProxyDaemonSetHook(),
		WithDSConfigMapHashAnnotationHook(util.DefaultNamespace, util.CinderConfigName, configMapInformer),
		csidrivernodeservicecontroller.WithCABundleDaemonSetHook(
			util.DefaultNamespace,
			trustedCAConfigMap,
			configMapInformer,
		),
	).WithServiceMonitorController(
		"CinderServiceMonitorController",
		dynamicClient,
		assets.ReadFile,
		"servicemonitor.yaml",
	).WithStorageClassController(
		"CinderServiceStorageClassController",
		assets.ReadFile,
		[]string{
			"storageclass.yaml",
		},
		kubeClient,
		kubeInformersForNamespaces.InformersFor(""),
		operatorInformers,
	)

	configSyncController := config.NewConfigSyncController(
		operatorClient,
		kubeClient,
		kubeInformersForNamespaces,
		configInformers,
		resyncInterval,
		controllerConfig.EventRecorder)

	klog.Info("Starting the informers")
	go kubeInformersForNamespaces.Start(ctx.Done())
	go dynamicInformers.Start(ctx.Done())
	go configInformers.Start(ctx.Done())
	go operatorInformers.Start(ctx.Done())

	klog.Info("Starting controllers")
	go csiControllerSet.Run(ctx, 1)
	go configSyncController.Run(ctx, 1)

	<-ctx.Done()

	return fmt.Errorf("stopped")
}
