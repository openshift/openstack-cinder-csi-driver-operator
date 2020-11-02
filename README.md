# Cinder CSI driver operator

An operator to deploy the [OpenStack Cinder CSI driver](https://github.com/openshift/cloud-provider-openstack/tree/master/pkg/csi/cinder) in OpenShift.

## Design

The operator is based on [openshift/library-go](https://github.com/openshift/library-go) and manages `ClusterCSIDriver` instance named `cinder.csi.openstack.org`.

# Usage

The operator is installed by default by cluster-storage-operator when OpenShift is installed on OpenStack. Deployment YAML files in `manifests/` directory are only for quick & dirty startup, the authoritative manifests are in [cluster-storage-operator project](https://github.com/openshift/cluster-storage-operator/tree/master/assets/csidriveroperators/openstack-cinder).
