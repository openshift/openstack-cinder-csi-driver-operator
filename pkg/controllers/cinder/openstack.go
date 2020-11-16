package cinder

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/blockstorage/v3/volumetypes"
	"github.com/gophercloud/utils/openstack/clientconfig"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
	"github.com/openshift/openstack-cinder-csi-driver-operator/pkg/util"
	"sigs.k8s.io/yaml"
)

type openStackClient struct {
	cloud *clientconfig.Cloud
}

func NewOpenStackClient(
	cloudConfigFilename string,
	informers v1helpers.KubeInformersForNamespaces,
) (*openStackClient, error) {
	cloud, err := getCloudFromFile(cloudConfigFilename)
	if err != nil {
		return nil, err
	}
	return &openStackClient{
		cloud: cloud,
	}, nil
}

func (o *openStackClient) GetVolumeTypes() ([]volumetypes.VolumeType, error) {
	clientOpts := new(clientconfig.ClientOpts)

	if o.cloud.AuthInfo != nil {
		clientOpts.AuthInfo = o.cloud.AuthInfo
		clientOpts.AuthType = o.cloud.AuthType
		clientOpts.Cloud = o.cloud.Cloud
		clientOpts.RegionName = o.cloud.RegionName
	}

	opts, err := clientconfig.AuthOptions(clientOpts)
	if err != nil {
		return nil, err
	}

	provider, err := openstack.NewClient(opts.IdentityEndpoint)
	if err != nil {
		return nil, err
	}

	cert, err := getCloudProviderCert()
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to get cloud provider CA certificate: %v", err)
	}

	if len(cert) > 0 {
		certPool, err := x509.SystemCertPool()
		if err != nil {
			return nil, fmt.Errorf("create system cert pool failed: %v", err)
		}
		certPool.AppendCertsFromPEM(cert)
		client := http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					RootCAs: certPool,
				},
			},
		}
		provider.HTTPClient = client
	}

	err = openstack.Authenticate(provider, *opts)
	if err != nil {
		return nil, err
	}

	client, err := openstack.NewSharedFileSystemV2(provider, gophercloud.EndpointOpts{
		Region: clientOpts.RegionName,
	})
	if err != nil {
		return nil, err
	}

	allPages, err := volumetypes.List(client, &volumetypes.ListOpts{}).AllPages()
	if err != nil {
		return nil, err
	}

	return volumetypes.ExtractVolumeTypes(allPages)
}

func getCloudFromFile(filename string) (*clientconfig.Cloud, error) {
	cloudConfig, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	var clouds clientconfig.Clouds
	err = yaml.Unmarshal(cloudConfig, &clouds)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal clouds credentials from %s: %v", filename, err)
	}

	cfg, ok := clouds.Clouds[util.CloudName]
	if !ok {
		return nil, fmt.Errorf("could not find cloud named %q in credential file %s", util.CloudName, filename)
	}
	return &cfg, nil
}

func getCloudProviderCert() ([]byte, error) {
	return ioutil.ReadFile(util.CertFile)
}
