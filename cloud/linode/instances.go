package linode

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/linode/linode-cloud-controller-manager/sentry"
	"github.com/linode/linodego"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	cloudprovider "k8s.io/cloud-provider"
)

type nodeCache struct {
	sync.RWMutex
	nodes      map[int]*linodego.Instance
	lastUpdate time.Time
	ttl        time.Duration
}

func (nc *nodeCache) refresh(ctx context.Context, client Client) error {
	nc.Lock()
	defer nc.Unlock()

	if time.Since(nc.lastUpdate) < nc.ttl {
		return nil
	}

	nc.nodes = make(map[int]*linodego.Instance)
	instances, err := client.ListInstances(ctx, nil)
	if err != nil {
		return err
	}
	for _, instance := range instances {
		instance := instance
		nc.nodes[instance.ID] = &instance
	}
	nc.lastUpdate = time.Now()

	return nil
}

type instances struct {
	client Client

	nodeCache *nodeCache
}

func newInstances(client Client) cloudprovider.InstancesV2 {
	return &instances{client, &nodeCache{
		nodes: make(map[int]*linodego.Instance),
		ttl:   90 * time.Second,
	}}
}

type instanceNoIPAddressesError struct {
	id int
}

func (e instanceNoIPAddressesError) Error() string {
	return fmt.Sprintf("instance %d has no IP addresses", e.id)
}

func (i *instances) linodeByName(nodeName types.NodeName) (*linodego.Instance, error) {
	i.nodeCache.RLock()
	defer i.nodeCache.RUnlock()
	for _, node := range i.nodeCache.nodes {
		if node.Label == string(nodeName) {
			return node, nil
		}
	}

	return nil, cloudprovider.InstanceNotFound
}

func (i *instances) linodeByID(id int) (*linodego.Instance, error) {
	i.nodeCache.RLock()
	defer i.nodeCache.RUnlock()
	instance, ok := i.nodeCache.nodes[id]
	if !ok {
		return nil, cloudprovider.InstanceNotFound
	}
	return instance, nil
}

func (i *instances) lookupLinode(ctx context.Context, node *v1.Node) (*linodego.Instance, error) {
	i.nodeCache.refresh(ctx, i.client)

	providerID := node.Spec.ProviderID
	nodeName := types.NodeName(node.Name)

	sentry.SetTag(ctx, "provider_id", providerID)
	sentry.SetTag(ctx, "node_name", node.Name)

	if providerID != "" {
		id, err := parseProviderID(providerID)
		if err != nil {
			sentry.CaptureError(ctx, err)
			return nil, err
		}
		sentry.SetTag(ctx, "linode_id", strconv.Itoa(id))

		return i.linodeByID(id)
	}

	return i.linodeByName(nodeName)
}

func (i *instances) InstanceExists(ctx context.Context, node *v1.Node) (bool, error) {
	ctx = sentry.SetHubOnContext(ctx)
	if _, err := i.lookupLinode(ctx, node); err != nil {
		if err == cloudprovider.InstanceNotFound {
			return false, nil
		}
		sentry.CaptureError(ctx, err)
		return false, err
	}

	return true, nil
}

func (i *instances) InstanceShutdown(ctx context.Context, node *v1.Node) (bool, error) {
	ctx = sentry.SetHubOnContext(ctx)
	instance, err := i.lookupLinode(ctx, node)
	if err != nil {
		sentry.CaptureError(ctx, err)
		return false, err
	}

	// An instance is considered to be "shutdown" when it is
	// in the process of shutting down, or already offline.
	if instance.Status == linodego.InstanceOffline ||
		instance.Status == linodego.InstanceShuttingDown {
		return true, nil
	}

	return false, nil
}

func (i *instances) InstanceMetadata(ctx context.Context, node *v1.Node) (*cloudprovider.InstanceMetadata, error) {
	ctx = sentry.SetHubOnContext(ctx)
	linode, err := i.lookupLinode(ctx, node)
	if err != nil {
		sentry.CaptureError(ctx, err)
		return nil, err
	}

	if len(linode.IPv4) == 0 {
		err := instanceNoIPAddressesError{linode.ID}
		sentry.CaptureError(ctx, err)
		return nil, err
	}

	addresses := []v1.NodeAddress{{Type: v1.NodeHostName, Address: linode.Label}}

	for _, ip := range linode.IPv4 {
		ipType := v1.NodeExternalIP
		if ip.IsPrivate() {
			ipType = v1.NodeInternalIP
		}
		addresses = append(addresses, v1.NodeAddress{Type: ipType, Address: ip.String()})
	}

	// note that Zone is omitted as it's not a thing in Linode
	meta := &cloudprovider.InstanceMetadata{
		ProviderID:    fmt.Sprintf("%v%v", providerIDPrefix, linode.ID),
		NodeAddresses: addresses,
		InstanceType:  linode.Type,
		Region:        linode.Region,
	}

	return meta, nil
}
