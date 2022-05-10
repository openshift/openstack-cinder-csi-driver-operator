package config

import (
	"strings"
	"testing"

	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/format"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestTranslateConfigMap(t *testing.T) {
	format.MaxDepth = 100
	format.TruncatedDiff = false

	tc := []struct {
		name                string
		source              string
		target              string
		isMultiAZDeployment bool
		errMsg              string
	}{
		{
			name: "Config with unsupported secret-namespace override",
			source: `[Global]
secret-namespace = foo
secret-name = openstack-credentials`,
			errMsg: "'[Global] secret-namespace' is set to a non-default value",
		}, {
			name: "Config with unsupported secret-name override",
			source: `[Global]
secret-namespace = kube-system
secret-name = foo`,
			errMsg: "'[Global] secret-name' is set to a non-default value",
		}, {
			name: "Config with unsupported kubeconfig-path override",
			source: `[Global]
secret-namespace = kube-system
secret-name = openstack-credentials
kubeconfig-path = https://foo`,
			errMsg: "'[Global] kubeconfig-path' is set to a non-default value",
		}, {
			name:   "Empty config",
			source: "",
			target: `[Global]
use-clouds  = true
clouds-file = /etc/kubernetes/secret/clouds.yaml
cloud       = openstack

[BlockStorage]
ignore-volume-az = no`,
		}, {
			name: "Non-empty config",
			source: `[BlockStorage]
trust-device-path = /dev/sdb1

[Global]
secret-name = openstack-credentials
secret-namespace = kube-system`,
			target: `[BlockStorage]
ignore-volume-az = no

[Global]
use-clouds  = true
clouds-file = /etc/kubernetes/secret/clouds.yaml
cloud       = openstack`,
		}, {
			name: "Multi-AZ deployment",
			source: `
[BlockStorage]
trust-device-path = /dev/sdb1`,
			isMultiAZDeployment: true,
			target: `[BlockStorage]
ignore-volume-az = yes

[Global]
use-clouds  = true
clouds-file = /etc/kubernetes/secret/clouds.yaml
cloud       = openstack`,
		}, {
			name: "User-provided AZ configuration is not overridden",
			source: `
[BlockStorage]
trust-device-path = /dev/sdb1
ignore-volume-az = yes`,
			isMultiAZDeployment: false,
			target: `[BlockStorage]
ignore-volume-az = yes

[Global]
use-clouds  = true
clouds-file = /etc/kubernetes/secret/clouds.yaml
cloud       = openstack`,
		},
	}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			sourceConfigMap := corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cloud-provider-config",
					Namespace: "openshift-config",
				},
				Data: map[string]string{
					"config": tc.source,
				},
			}
			expectedConfigMap := corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cinder-csi-config",
					Namespace: "openshift-cluster-csi-drivers",
				},
				Data: map[string]string{
					"config": tc.target,
				},
			}
			actualConfigMap, err := translateConfigMap(&sourceConfigMap, tc.isMultiAZDeployment)
			if tc.errMsg != "" {
				g.Expect(err).Should(MatchError(tc.errMsg))
				return
			} else {
				g.Expect(err).ToNot(HaveOccurred())
				// The output is unsorted so we must reload and reparse the
				// strings
				expected, _ := expectedConfigMap.Data[sourceConfigKey]
				actual, _ := actualConfigMap.Data[targetConfigKey]
				g.Expect(err).ToNot(HaveOccurred())

				actual = strings.TrimSpace(actual)
				g.Expect(expected).Should(Equal(actual))
			}
		})
	}
}
