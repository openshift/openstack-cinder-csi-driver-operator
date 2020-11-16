package cinder

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gophercloud/gophercloud/openstack/blockstorage/v3/volumetypes"
	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/resource/resourceapply"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
	"github.com/openshift/openstack-cinder-csi-driver-operator/pkg/util"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/kubernetes"
	storagelisters "k8s.io/client-go/listers/storage/v1"
	"k8s.io/klog/v2"
)

// This CinderController watches OpenStack and:
// 1) Installs Cinder CSI driver itself.
// 2) Creates StorageClass for each volume type provided by Cinder.
//
// Note that StorageClasses are not deleted when a volumes type disappears
// from Cinder.

type CinderController struct {
	operatorClient     v1helpers.OperatorClient
	kubeClient         kubernetes.Interface
	openStackClient    *openStackClient
	storageClassLister storagelisters.StorageClassLister
	// Controllers to start when Cinder is detected
	csiControllers     []Runnable
	controllersRunning bool
	eventRecorder      events.Recorder
}

type Runnable interface {
	Run(ctx context.Context, workers int)
}

const (
	// Minimal interval between controller resyncs. The controller will detect
	// new share types in Cinder and create StorageClasses for them at least
	// once per this interval.
	resyncInterval = 20 * time.Minute

	operatorConditionPrefix = "CinderController"
)

func NewCinderController(
	operatorClient v1helpers.OperatorClient,
	kubeClient kubernetes.Interface,
	informers v1helpers.KubeInformersForNamespaces,
	openStackClient *openStackClient,
	csiControllers []Runnable,
	eventRecorder events.Recorder) factory.Controller {

	scInformer := informers.InformersFor("").Storage().V1().StorageClasses()
	c := &CinderController{
		operatorClient:     operatorClient,
		kubeClient:         kubeClient,
		storageClassLister: scInformer.Lister(),
		openStackClient:    openStackClient,
		csiControllers:     csiControllers,
		eventRecorder:      eventRecorder.WithComponentSuffix("CinderController"),
	}
	return factory.New().WithSync(c.sync).WithSyncDegradedOnError(operatorClient).ResyncEvery(resyncInterval).WithInformers(
		operatorClient.Informer(),
		scInformer.Informer(),
	).ToController("CinderController", eventRecorder)
}

func (c *CinderController) sync(ctx context.Context, syncCtx factory.SyncContext) error {
	klog.V(4).Infof("Cinder sync started")
	defer klog.V(4).Infof("Cinder sync finished")

	opSpec, _, _, err := c.operatorClient.GetOperatorState()
	if err != nil {
		return err
	}
	if opSpec.ManagementState != operatorv1.Managed {
		return nil
	}

	volumeTypes, err := c.openStackClient.GetVolumeTypes()
	if err != nil {
		return err
	}

	if len(volumeTypes) == 0 {
		err = fmt.Errorf("no volume types found for Cinder service")
		klog.V(2).Error(err, "No volume types found for Cinder service")
		return err
	}

	if !c.controllersRunning {
		klog.V(4).Infof("Starting CSI driver controllers")
		for _, ctrl := range c.csiControllers {
			go ctrl.Run(ctx, 1)
		}
		c.controllersRunning = true
	}
	err = c.syncStorageClasses(ctx, volumeTypes)
	if err != nil {
		return err
	}

	return nil
}

func (c *CinderController) syncStorageClasses(ctx context.Context, volumeTypes []volumetypes.VolumeType) error {
	var errs []error
	for _, volumeType := range volumeTypes {
		klog.V(4).Infof("Syncing storage class for volumeType type %s", volumeType.Name)
		sc := c.generateStorageClass(volumeType)
		_, _, err := resourceapply.ApplyStorageClass(c.kubeClient.StorageV1(), c.eventRecorder, sc)
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.NewAggregate(errs)
}

func (c *CinderController) applyStorageClass(ctx context.Context, expected *storagev1.StorageClass) error {
	_, _, err := resourceapply.ApplyStorageClass(c.kubeClient.StorageV1(), c.eventRecorder, expected)
	return err
}

func (c *CinderController) generateStorageClass(volumeType volumetypes.VolumeType) *storagev1.StorageClass {
	storageClassName := util.StorageClassNamePrefix + strings.ToLower(volumeType.Name)
	delete := corev1.PersistentVolumeReclaimDelete
	immediate := storagev1.VolumeBindingImmediate
	sc := &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: storageClassName,
		},
		Provisioner: "cinder.csi.openstack.org",
		Parameters: map[string]string{
			"type": volumeType.Name,
		},
		ReclaimPolicy:     &delete,
		VolumeBindingMode: &immediate,
	}
	return sc
}
