FROM registry.ci.openshift.org/ocp/builder:rhel-8-golang-1.16-openshift-4.8 AS builder
WORKDIR /go/src/github.com/openshift/openstack-cinder-csi-driver-operator
COPY . .
RUN make

FROM registry.ci.openshift.org/ocp/4.8:base
COPY --from=builder /go/src/github.com/openshift/openstack-cinder-csi-driver-operator/openstack-cinder-csi-driver-operator /usr/bin/
COPY manifests /manifests
ENTRYPOINT ["/usr/bin/openstack-cinder-csi-driver-operator"]
LABEL io.k8s.display-name="OpenShift OpenStack Cinder CSI Driver Operator" \
	io.k8s.description="The OpenStack Cinder CSI Driver Operator installs and maintains the OpenStack Cinder CSI Driver on a cluster."
