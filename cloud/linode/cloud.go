package linode

import (
	"fmt"
	"io"
	"os"

	"github.com/linode/linodego"
	"github.com/spf13/pflag"
	"k8s.io/client-go/informers"
	cloudprovider "k8s.io/cloud-provider"
)

const (
	// The name of this cloudprovider
	ProviderName   = "linode"
	accessTokenEnv = "LINODE_API_TOKEN"
	regionEnv      = "LINODE_REGION"
	urlEnv         = "LINODE_URL"
)

// Options is a configuration object for this cloudprovider implementation.
// We expect it to be initialized with flags external to this package, likely in
// main.go
var Options struct {
	KubeconfigFlag *pflag.Flag
	LinodeGoDebug  bool
}

type linodeCloud struct {
	client        Client
	instances     cloudprovider.InstancesV2
	loadbalancers cloudprovider.LoadBalancer
}

func init() {
	cloudprovider.RegisterCloudProvider(
		ProviderName,
		func(io.Reader) (cloudprovider.Interface, error) {
			return newCloud()
		})
}

func newCloud() (cloudprovider.Interface, error) {
	// Read environment variables (from secrets)
	apiToken := os.Getenv(accessTokenEnv)
	if apiToken == "" {
		return nil, fmt.Errorf("%s must be set in the environment (use a k8s secret)", accessTokenEnv)
	}

	region := os.Getenv(regionEnv)
	if region == "" {
		return nil, fmt.Errorf("%s must be set in the environment (use a k8s secret)", regionEnv)
	}

	url := os.Getenv(urlEnv)
	ua := fmt.Sprintf("linode-cloud-controller-manager %s", linodego.DefaultUserAgent)

	linodeClient, err := newLinodeClient(apiToken, ua, url)
	if err != nil {
		return nil, fmt.Errorf("client was not created succesfully: %w", err)
	}

	if Options.LinodeGoDebug {
		linodeClient.SetDebug(true)
	}

	// Return struct that satisfies cloudprovider.Interface
	return &linodeCloud{
		client:        linodeClient,
		instances:     newInstances(linodeClient),
		loadbalancers: newLoadbalancers(linodeClient, region),
	}, nil
}

func (c *linodeCloud) Initialize(clientBuilder cloudprovider.ControllerClientBuilder, stopCh <-chan struct{}) {
	kubeclient := clientBuilder.ClientOrDie("linode-shared-informers")
	sharedInformer := informers.NewSharedInformerFactory(kubeclient, 0)
	serviceInformer := sharedInformer.Core().V1().Services()
	nodeInformer := sharedInformer.Core().V1().Nodes()

	serviceController := newServiceController(c.loadbalancers.(*loadbalancers), serviceInformer)
	go serviceController.Run(stopCh)

	nodeController := newNodeController(kubeclient, c.client, nodeInformer)
	go nodeController.Run(stopCh)
}

func (c *linodeCloud) LoadBalancer() (cloudprovider.LoadBalancer, bool) {
	return c.loadbalancers, true
}

func (c *linodeCloud) Instances() (cloudprovider.Instances, bool) {
	return nil, false
}

func (c *linodeCloud) InstancesV2() (cloudprovider.InstancesV2, bool) {
	return c.instances, true
}

func (c *linodeCloud) Zones() (cloudprovider.Zones, bool) {
	return nil, false
}

func (c *linodeCloud) Clusters() (cloudprovider.Clusters, bool) {
	return nil, false
}

func (c *linodeCloud) Routes() (cloudprovider.Routes, bool) {
	return nil, false
}

func (c *linodeCloud) ProviderName() string {
	return ProviderName
}

func (c *linodeCloud) ScrubDNS(_, _ []string) (nsOut, srchOut []string) {
	return nil, nil
}

func (c *linodeCloud) HasClusterID() bool {
	return true
}
