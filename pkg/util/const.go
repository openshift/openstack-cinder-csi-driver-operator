package util

const (
	CloudConfigNamespace = "openshift-config"
	CloudConfigName      = "cloud-provider-config"

	StorageClassNamePrefix = "csi-cinder-"

	// OpenStack config file name (as present in the operator Deployment)
	CloudConfigFilename = "/etc/openstack/clouds.yaml"
	CertFile            = "/etc/openstack-ca/ca-bundle.pem"

	// Name of cloud in secret provided by cloud-credentials-operator
	CloudName = "openstack"
)
