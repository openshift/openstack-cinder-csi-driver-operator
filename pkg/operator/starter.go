package operator

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	kubeclient "k8s.io/client-go/kubernetes"
	corev1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	opv1 "github.com/openshift/api/operator/v1"
	configclient "github.com/openshift/client-go/config/clientset/versioned"
	configinformers "github.com/openshift/client-go/config/informers/externalversions"
	"github.com/openshift/library-go/pkg/controller/controllercmd"
	"github.com/openshift/library-go/pkg/operator/csi/csicontrollerset"
	"github.com/openshift/library-go/pkg/operator/csi/csidrivercontrollerservicecontroller"
	"github.com/openshift/library-go/pkg/operator/csi/csidrivernodeservicecontroller"
	goc "github.com/openshift/library-go/pkg/operator/genericoperatorclient"
	"github.com/openshift/library-go/pkg/operator/v1helpers"

	"github.com/openshift/openstack-cinder-csi-driver-operator/pkg/generated"
)

const (
	// Operand and operator run in the same namespace
	defaultNamespace = "openshift-cluster-csi-drivers"
	operatorName     = "openstack-cinder-csi-driver-operator"
	operandName      = "openstack-cinder-csi-driver"
	instanceName     = "cinder.csi.openstack.org"
	secretName       = "openstack-cloud-credentials"

	customConfigNamespace = defaultNamespace
	customConfigName      = "openstack-cinder-custom-config"
	customConfigKey       = "cloud.conf"
)

func RunOperator(ctx context.Context, controllerConfig *controllercmd.ControllerContext) error {
	// Create clientsets and informers
	kubeClient := kubeclient.NewForConfigOrDie(rest.AddUserAgent(controllerConfig.KubeConfig, operatorName))
	kubeInformersForNamespaces := v1helpers.NewKubeInformersForNamespaces(kubeClient, defaultNamespace, "")
	secretInformer := kubeInformersForNamespaces.InformersFor(defaultNamespace).Core().V1().Secrets()

	// Create config clientset and informer. This is used to get the cluster ID
	configClient := configclient.NewForConfigOrDie(rest.AddUserAgent(controllerConfig.KubeConfig, operatorName))
	configInformers := configinformers.NewSharedInformerFactory(configClient, 20*time.Minute)

	// Create custom config lister
	customConfigInformer := kubeInformersForNamespaces.InformersFor(customConfigNamespace).Core().V1().ConfigMaps()
	customConfigLister := customConfigInformer.Lister().ConfigMaps(customConfigNamespace)

	// Create GenericOperatorclient. This is used by the library-go controllers created down below
	gvr := opv1.SchemeGroupVersion.WithResource("clustercsidrivers")
	operatorClient, dynamicInformers, err := goc.NewClusterScopedOperatorClientWithConfigName(controllerConfig.KubeConfig, gvr, instanceName)
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
		kubeInformersForNamespaces,
		generated.Asset,
		[]string{
			"configmap.yaml",
			"storageclass.yaml",
			"csidriver.yaml",
			"controller_sa.yaml",
			"node_sa.yaml",
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
		},
	).WithCSIConfigObserverController(
		"OpenStackCinderDriverCSIConfigObserverController",
		configInformers,
	).WithCSIDriverControllerService(
		"OpenStackCinderDriverControllerServiceController",
		generated.MustAsset,
		"controller.yaml",
		kubeClient,
		kubeInformersForNamespaces.InformersFor(defaultNamespace),
		nil,
		csidrivercontrollerservicecontroller.WithSecretHashAnnotationHook(defaultNamespace, secretName, secretInformer),
		csidrivercontrollerservicecontroller.WithObservedProxyDeploymentHook(),
		withCustomConfigDeploymentHook(customConfigLister),
	).WithCSIDriverNodeService(
		"OpenStackCinderDriverNodeServiceController",
		generated.MustAsset,
		"node.yaml",
		kubeClient,
		kubeInformersForNamespaces.InformersFor(defaultNamespace),
		csidrivernodeservicecontroller.WithObservedProxyDaemonSetHook(),
		withCustomConfigDaemonSetHook(customConfigLister),
	).WithExtraInformers(configInformers.Config().V1().Proxies().Informer(), secretInformer.Informer())

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
func withCustomConfigDeploymentHook(cloudConfigLister corev1listers.ConfigMapNamespaceLister) csidrivercontrollerservicecontroller.DeploymentHookFunc {
	return func(_ *opv1.OperatorSpec, deployment *appsv1.Deployment) error {
		switch used, err := isCustomConfigUsed(cloudConfigLister); {
		case err != nil:
			return fmt.Errorf("could not determine if a custom Cinder CSI driver config is in use: %w", err)
		case !used:
			return nil
		}
		deployment.Spec.Template.Spec.Volumes = append(deployment.Spec.Template.Spec.Volumes, corev1.Volume{
			Name: "custom-config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: customConfigName},
				},
			},
		})
		for i := range deployment.Spec.Template.Spec.Containers {
			container := &deployment.Spec.Template.Spec.Containers[i]
			if container.Name != "csi-driver" {
				continue
			}
			container.Env = append(container.Env, corev1.EnvVar{
				Name:  "CLOUD_CONFIG",
				Value: "/etc/kubernetes/custom-config/cloud.conf",
			})
			container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
				Name:      "custom-config",
				MountPath: "/etc/kubernetes/custom-config",
				ReadOnly:  true,
			})
			return nil
		}
		return fmt.Errorf("could not use custom config because the csi-driver container is missing from the deployment")
	}
}

// withCustomConfigDaemonSetHook executes the asset as a template to fill out the parts required
// when using a custom config with node controller daemonset.
func withCustomConfigDaemonSetHook(cloudConfigLister corev1listers.ConfigMapNamespaceLister) csidrivernodeservicecontroller.DaemonSetHookFunc {
	return func(_ *opv1.OperatorSpec, daemonset *appsv1.DaemonSet) error {
		switch used, err := isCustomConfigUsed(cloudConfigLister); {
		case err != nil:
			return fmt.Errorf("could not determine if a custom Cinder CSI driver config is in use: %w", err)
		case !used:
			return nil
		}
		daemonset.Spec.Template.Spec.Volumes = append(daemonset.Spec.Template.Spec.Volumes, corev1.Volume{
			Name: "custom-config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: customConfigName},
				},
			},
		})
		for i := range daemonset.Spec.Template.Spec.Containers {
			container := &daemonset.Spec.Template.Spec.Containers[i]
			if container.Name != "csi-driver" {
				continue
			}
			container.Env = append(container.Env, corev1.EnvVar{
				Name:  "CLOUD_CONFIG",
				Value: "/etc/kubernetes/custom-config/cloud.conf",
			})
			container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
				Name:      "custom-config",
				MountPath: "/etc/kubernetes/custom-config",
				ReadOnly:  true,
			})
			return nil
		}
		return fmt.Errorf("could not use custom config because the csi-driver container is missing from the deployment")
	}
}

// isCustomConfigUsed returns true if the cloud config ConfigMap exists and contains a custom Cinder CSI driver config.
func isCustomConfigUsed(cloudConfigLister corev1listers.ConfigMapNamespaceLister) (bool, error) {
	cloudConfigCM, err := cloudConfigLister.Get(customConfigName)
	if errors.IsNotFound(err) {
		// no cloud config ConfigMap so there is no custom config
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to get the %s/%s ConfigMap: %w", customConfigNamespace, customConfigName, err)
	}
	_, exists := cloudConfigCM.Data[customConfigKey]
	return exists, nil
}
