module github.com/openshift/openstack-cinder-csi-driver-operator

go 1.14

require (
	github.com/go-bindata/go-bindata v3.1.2+incompatible
	github.com/openshift/api v0.0.0-20200827090112-c05698d102cf
	github.com/openshift/build-machinery-go v0.0.0-20200819073603-48aa266c95f7
	github.com/openshift/library-go v0.0.0-20200909090707-a59cf35af612
	github.com/prometheus/client_golang v1.7.1
	github.com/spf13/cobra v1.0.0
	github.com/spf13/pflag v1.0.5
	k8s.io/apimachinery v0.19.0
	k8s.io/client-go v0.19.0
	k8s.io/component-base v0.19.0
	k8s.io/klog/v2 v2.3.0
)
