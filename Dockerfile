FROM registry.ci.openshift.org/ocp/builder:rhel-9-golang-1.22-openshift-4.17 AS builder
WORKDIR /go/src/github.com/openshift/openstack-cinder-csi-driver-operator
COPY . .
RUN make

FROM registry.ci.openshift.org/ocp/4.17:base-rhel9
COPY --from=builder /go/src/github.com/openshift/openstack-cinder-csi-driver-operator/openstack-cinder-csi-driver-operator /usr/bin/
ENTRYPOINT ["/usr/bin/openstack-cinder-csi-driver-operator"]
LABEL io.k8s.display-name="OpenShift OpenStack Cinder CSI Driver Operator" \
	io.k8s.description="The OpenStack Cinder CSI Driver Operator installs and maintains the OpenStack Cinder CSI Driver on a cluster."
