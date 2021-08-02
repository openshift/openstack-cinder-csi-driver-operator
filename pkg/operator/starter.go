package operator

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/client-go/dynamic"
	kubeclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	opv1 "github.com/openshift/api/operator/v1"
	configclient "github.com/openshift/client-go/config/clientset/versioned"
	configinformers "github.com/openshift/client-go/config/informers/externalversions"
	"github.com/openshift/library-go/pkg/controller/controllercmd"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/csi/csicontrollerset"
	"github.com/openshift/library-go/pkg/operator/csi/csidrivercontrollerservicecontroller"
	"github.com/openshift/library-go/pkg/operator/csi/csidrivernodeservicecontroller"
	"github.com/openshift/library-go/pkg/operator/deploymentcontroller"
	goc "github.com/openshift/library-go/pkg/operator/genericoperatorclient"
	"github.com/openshift/library-go/pkg/operator/v1helpers"

	"github.com/openshift/openstack-cinder-csi-driver-operator/assets"
)

const (
	// Operand and operator run in the same namespace
	defaultNamespace = "openshift-cluster-csi-drivers"
	operatorName     = "openstack-cinder-csi-driver-operator"
	operandName      = "openstack-cinder-csi-driver"
	instanceName     = "cinder.csi.openstack.org"
	secretName       = "openstack-cloud-credentials"

	multiAZConfigPath = "/etc/kubernetes/config/multiaz-cloud.conf"
)

func RunOperator(ctx context.Context, controllerConfig *controllercmd.ControllerContext) error {
	// Create clientsets and informers
	kubeClient := kubeclient.NewForConfigOrDie(rest.AddUserAgent(controllerConfig.KubeConfig, operatorName))
	kubeInformersForNamespaces := v1helpers.NewKubeInformersForNamespaces(kubeClient, defaultNamespace, "")
	secretInformer := kubeInformersForNamespaces.InformersFor(defaultNamespace).Core().V1().Secrets()

	// Create config clientset and informer. This is used to get the cluster ID
	configClient := configclient.NewForConfigOrDie(rest.AddUserAgent(controllerConfig.KubeConfig, operatorName))
	configInformers := configinformers.NewSharedInformerFactory(configClient, 20*time.Minute)

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

	ci, err := getCloudInfo()
	if err != nil {
		return fmt.Errorf("couldn't collect info about cloud availability zones: %w", err)
	}
	// We consider a cloud multiaz when it either have several different zones or compute and volumes
	// are different.
	isMultiAZDeployment := len(ci.ComputeZones) > 1 || len(ci.VolumeZones) > 1 || ci.ComputeZones[0] != ci.VolumeZones[0]

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
			"configmap.yaml",
			"storageclass.yaml",
			"volumesnapshotclass.yaml",
			"csidriver.yaml",
			"controller_sa.yaml",
			"node_sa.yaml",
			"service.yaml",
			"rbac/attacher_role.yaml",
			"rbac/attacher_binding.yaml",
			"rbac/privileged_role.yaml",
			"rbac/controller_privileged_binding.yaml",
			"rbac/node_privileged_binding.yaml",
			"rbac/provisioner_role.yaml",
			"rbac/provisioner_binding.yaml",
			"rbac/resizer_role.yaml",
			"rbac/resizer_binding.yaml",
			"rbac/snapshotter_role.yaml",
			"rbac/snapshotter_binding.yaml",
			"rbac/kube_rbac_proxy_role.yaml",
			"rbac/kube_rbac_proxy_binding.yaml",
			"rbac/prometheus_role.yaml",
			"rbac/prometheus_rolebinding.yaml",
		},
	).WithCSIConfigObserverController(
		"OpenStackCinderDriverCSIConfigObserverController",
		configInformers,
	).WithCSIDriverControllerService(
		"OpenStackCinderDriverControllerServiceController",
		assets.ReadFile,
		"controller.yaml",
		kubeClient,
		kubeInformersForNamespaces.InformersFor(defaultNamespace),
		configInformers,
		[]factory.Informer{
			secretInformer.Informer(),
			configInformers.Config().V1().Proxies().Informer(),
		},
		csidrivercontrollerservicecontroller.WithSecretHashAnnotationHook(defaultNamespace, secretName, secretInformer),
		csidrivercontrollerservicecontroller.WithObservedProxyDeploymentHook(),
		withCustomConfigDeploymentHook(isMultiAZDeployment),
	).WithCSIDriverNodeService(
		"OpenStackCinderDriverNodeServiceController",
		assets.ReadFile,
		"node.yaml",
		kubeClient,
		kubeInformersForNamespaces.InformersFor(defaultNamespace),
		nil, // Node doesn't need to react to any changes
		csidrivernodeservicecontroller.WithObservedProxyDaemonSetHook(),
		withCustomConfigDaemonSetHook(isMultiAZDeployment),
	).WithServiceMonitorController(
		"CinderServiceMonitorController",
		dynamicClient,
		assets.ReadFile,
		"servicemonitor.yaml",
	)

	if err != nil {
		return err
	}

	klog.Info("Starting the informers")
	go kubeInformersForNamespaces.Start(ctx.Done())
	go dynamicInformers.Start(ctx.Done())
	go configInformers.Start(ctx.Done())

	klog.Info("Starting controllerset")
	go csiControllerSet.Run(ctx, 1)

	<-ctx.Done()

	return fmt.Errorf("stopped")
}

// withCustomConfigDeploymentHook executes the asset as a template to fill out the parts required
// when using a custom config with controller deployment.
func withCustomConfigDeploymentHook(isMultiAZDeployment bool) deploymentcontroller.DeploymentHookFunc {
	return func(_ *opv1.OperatorSpec, deployment *appsv1.Deployment) error {
		if !isMultiAZDeployment {
			return nil
		}

		for i := range deployment.Spec.Template.Spec.Containers {
			container := &deployment.Spec.Template.Spec.Containers[i]
			if container.Name != "csi-driver" {
				continue
			}
			for i, env := range container.Env {
				if env.Name == "CLOUD_CONFIG" {
					container.Env[i].Value = multiAZConfigPath
					break
				}
			}
			return nil
		}
		return fmt.Errorf("could not use multiaz config because the csi-driver container is missing from the deployment")
	}
}

// withCustomConfigDaemonSetHook executes the asset as a template to fill out the parts required
// when using a custom config with node controller daemonset.
func withCustomConfigDaemonSetHook(isMultiAZDeployment bool) csidrivernodeservicecontroller.DaemonSetHookFunc {
	return func(_ *opv1.OperatorSpec, daemonset *appsv1.DaemonSet) error {
		if !isMultiAZDeployment {
			return nil
		}

		for i := range daemonset.Spec.Template.Spec.Containers {
			container := &daemonset.Spec.Template.Spec.Containers[i]
			if container.Name != "csi-driver" {
				continue
			}
			for i, env := range container.Env {
				if env.Name == "CLOUD_CONFIG" {
					container.Env[i].Value = multiAZConfigPath
					break
				}
			}
			return nil
		}
		return fmt.Errorf("could not use multiaz config because the csi-driver container is missing from the deployment")
	}
}
