/*
Copyright 2020 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package azure

import (
	"context"
	"fmt"

	authz "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/authorization/armauthorization/v3"
	compute "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
	network "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork"
	azureresources "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"k8s.io/kops/pkg/resources"
	"k8s.io/kops/upup/pkg/fi"
	"k8s.io/kops/upup/pkg/fi/cloudup/azure"
	"k8s.io/utils/set"
)

const (
	typeResourceGroup            = "ResourceGroup"
	typeVirtualNetwork           = "VirtualNetwork"
	typeNetworkSecurityGroup     = "NetworkSecurityGroup"
	typeApplicationSecurityGroup = "ApplicationSecurityGroup"
	typeSubnet                   = "Subnet"
	typeRouteTable               = "RouteTable"
	typeVMScaleSet               = "VMScaleSet"
	typeDisk                     = "Disk"
	typeRoleAssignment           = "RoleAssignment"
	typeLoadBalancer             = "LoadBalancer"
	typePublicIPAddress          = "PublicIPAddress"
	typeNatGateway               = "NatGateway"
)

// ListResourcesAzure lists all resources for the cluster by quering Azure.
func ListResourcesAzure(cloud azure.AzureCloud, clusterInfo resources.ClusterInfo) (map[string]*resources.Resource, error) {
	g := resourceGetter{
		cloud:       cloud,
		clusterInfo: clusterInfo,
	}
	return g.listResourcesAzure()
}

type resourceGetter struct {
	cloud       azure.AzureCloud
	clusterInfo resources.ClusterInfo
}

func (g *resourceGetter) resourceGroupName() string {
	return g.clusterInfo.AzureResourceGroupName
}

func (g *resourceGetter) resourceGroupID() string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s", g.clusterInfo.AzureSubscriptionID, g.clusterInfo.AzureResourceGroupName)
}

func (g *resourceGetter) storageAccountID() string {
	return g.clusterInfo.AzureStorageAccountID
}

func (g *resourceGetter) listResourcesAzure() (map[string]*resources.Resource, error) {
	rs, err := g.listAll()
	if err != nil {
		return nil, err
	}

	// Convert a slice of resources to a map of resources keyed by type and ID.
	resources := make(map[string]*resources.Resource)
	for _, r := range rs {
		if r.Done {
			continue
		}
		resources[toKey(r.Type, r.ID)] = r
	}
	return resources, nil
}

// listAll list all resources owned by kops for the cluster.
//
// TODO(kenji): Set the "Shared" field of each resource so that we won't delete
// shared resources.
func (g *resourceGetter) listAll() ([]*resources.Resource, error) {
	fns := []func(ctx context.Context) ([]*resources.Resource, error){
		g.listResourceGroups,
		g.listVirtualNetworksAndSubnets,
		g.listNetworkSecurityGroups,
		g.listApplicationSecurityGroups,
		g.listRouteTables,
		g.listVMScaleSetsAndRoleAssignments,
		g.listDisks,
		g.listLoadBalancers,
		g.listPublicIPAddresses,
		g.listNatGateways,
	}

	var resources []*resources.Resource
	ctx := context.TODO()
	for _, fn := range fns {
		rs, err := fn(ctx)
		if err != nil {
			return nil, err
		}
		resources = append(resources, rs...)
	}
	return resources, nil
}

func (g *resourceGetter) listResourceGroups(ctx context.Context) ([]*resources.Resource, error) {
	rgs, err := g.cloud.ResourceGroup().List(ctx)
	if err != nil {
		return nil, err
	}

	var rs []*resources.Resource
	for _, rg := range rgs {
		if !g.isOwnedByCluster(rg.Tags) {
			continue
		}
		rs = append(rs, g.toResourceGroupResource(rg))
	}
	return rs, nil
}

func (g *resourceGetter) toResourceGroupResource(rg *azureresources.ResourceGroup) *resources.Resource {
	return &resources.Resource{
		Obj:     rg,
		Type:    typeResourceGroup,
		ID:      *rg.Name,
		Name:    *rg.Name,
		Deleter: g.deleteResourceGroup,
		Shared:  g.clusterInfo.AzureResourceGroupShared,
	}
}

func (g *resourceGetter) deleteResourceGroup(_ fi.Cloud, r *resources.Resource) error {
	return g.cloud.ResourceGroup().Delete(context.TODO(), r.Name)
}

func (g *resourceGetter) listVirtualNetworksAndSubnets(ctx context.Context) ([]*resources.Resource, error) {
	vnets, err := g.cloud.VirtualNetwork().List(ctx, g.resourceGroupName())
	if err != nil {
		return nil, err
	}

	var rs []*resources.Resource
	for _, vnet := range vnets {
		if !g.isOwnedByCluster(vnet.Tags) {
			continue
		}
		r, err := g.toVirtualNetworkResource(vnet)
		if err != nil {
			return nil, err
		}
		rs = append(rs, r)
		// Add all subnets belonging to the virtual network.
		subnets, err := g.listSubnets(ctx, *vnet.Name)
		if err != nil {
			return nil, err
		}
		rs = append(rs, subnets...)
	}
	return rs, nil
}

func (g *resourceGetter) toVirtualNetworkResource(vnet *network.VirtualNetwork) (*resources.Resource, error) {
	var blocks []string
	blocks = append(blocks, toKey(typeResourceGroup, g.resourceGroupName()))

	nsgs := set.New[string]()
	if vnet.Properties != nil && vnet.Properties.Subnets != nil {
		for _, sn := range vnet.Properties.Subnets {
			if sn.Properties == nil || sn.Properties.NetworkSecurityGroup == nil || sn.Properties.NetworkSecurityGroup.ID == nil {
				continue
			}
			nsgID, err := azure.ParseNetworkSecurityGroupID(*sn.Properties.NetworkSecurityGroup.ID)
			if err != nil {
				return nil, fmt.Errorf("parsing network security group ID: %s", err)
			}
			nsgs.Insert(nsgID.NetworkSecurityGroupName)
		}
	}
	for nsg := range nsgs {
		blocks = append(blocks, toKey(typeNetworkSecurityGroup, nsg))
	}

	return &resources.Resource{
		Obj:     vnet,
		Type:    typeVirtualNetwork,
		ID:      *vnet.Name,
		Name:    *vnet.Name,
		Deleter: g.deleteVirtualNetwork,
		Blocks:  blocks,
		Shared:  g.clusterInfo.AzureNetworkShared,
	}, nil
}

func (g *resourceGetter) deleteVirtualNetwork(_ fi.Cloud, r *resources.Resource) error {
	return g.cloud.VirtualNetwork().Delete(context.TODO(), g.resourceGroupName(), r.Name)
}

func (g *resourceGetter) listSubnets(ctx context.Context, vnetName string) ([]*resources.Resource, error) {
	subnets, err := g.cloud.Subnet().List(ctx, g.resourceGroupName(), vnetName)
	if err != nil {
		return nil, err
	}

	var rs []*resources.Resource
	for _, sn := range subnets {
		rs = append(rs, g.toSubnetResource(sn, vnetName))
	}
	return rs, nil
}

func (g *resourceGetter) toSubnetResource(subnet *network.Subnet, vnetName string) *resources.Resource {
	var blocks []string
	blocks = append(blocks, toKey(typeVirtualNetwork, vnetName))
	blocks = append(blocks, toKey(typeResourceGroup, g.resourceGroupName()))

	if subnet.Properties != nil && subnet.Properties.NatGateway != nil && subnet.Properties.NatGateway.ID != nil {
		blocks = append(blocks, toKey(typeNatGateway, *subnet.Properties.NatGateway.ID))
	}

	return &resources.Resource{
		Obj:  subnet,
		Type: typeSubnet,
		ID:   *subnet.Name,
		Name: *subnet.Name,
		Deleter: func(_ fi.Cloud, r *resources.Resource) error {
			return g.deleteSubnet(vnetName, r)
		},
		Blocks: blocks,
		Shared: g.clusterInfo.AzureNetworkShared,
	}
}

func (g *resourceGetter) deleteSubnet(vnetName string, r *resources.Resource) error {
	return g.cloud.Subnet().Delete(context.TODO(), g.resourceGroupName(), vnetName, r.Name)
}

func (g *resourceGetter) listNetworkSecurityGroups(ctx context.Context) ([]*resources.Resource, error) {
	NetworkSecurityGroups, err := g.cloud.NetworkSecurityGroup().List(ctx, g.resourceGroupName())
	if err != nil {
		return nil, err
	}

	var rs []*resources.Resource
	for i := range NetworkSecurityGroups {
		r, err := g.toNetworkSecurityGroupResource(NetworkSecurityGroups[i])
		if err != nil {
			return nil, err
		}
		rs = append(rs, r)
	}
	return rs, nil
}

func (g *resourceGetter) toNetworkSecurityGroupResource(NetworkSecurityGroup *network.SecurityGroup) (*resources.Resource, error) {
	var blocks []string
	blocks = append(blocks, toKey(typeResourceGroup, g.resourceGroupName()))

	asgs := set.New[string]()
	if NetworkSecurityGroup.Properties.SecurityRules != nil {
		for _, nsr := range NetworkSecurityGroup.Properties.SecurityRules {
			if nsr.Properties.SourceApplicationSecurityGroups != nil {
				for _, sasg := range nsr.Properties.SourceApplicationSecurityGroups {
					asgID, err := azure.ParseApplicationSecurityGroupID(*sasg.ID)
					if err != nil {
						return nil, fmt.Errorf("parsing application security group ID: %w", err)
					}
					asgs.Insert(asgID.ApplicationSecurityGroupName)
				}
			}
			if nsr.Properties.DestinationApplicationSecurityGroups != nil {
				for _, dasg := range nsr.Properties.DestinationApplicationSecurityGroups {
					asgID, err := azure.ParseApplicationSecurityGroupID(*dasg.ID)
					if err != nil {
						return nil, fmt.Errorf("parsing application security group ID: %w", err)
					}
					asgs.Insert(asgID.ApplicationSecurityGroupName)
				}
			}
		}
	}
	for asg := range asgs {
		blocks = append(blocks, toKey(typeApplicationSecurityGroup, asg))
	}

	return &resources.Resource{
		Obj:  NetworkSecurityGroup,
		Type: typeNetworkSecurityGroup,
		ID:   *NetworkSecurityGroup.Name,
		Name: *NetworkSecurityGroup.Name,
		Deleter: func(_ fi.Cloud, r *resources.Resource) error {
			return g.deleteNetworkSecurityGroup(r)
		},
		Blocks: blocks,
	}, nil
}

func (g *resourceGetter) deleteNetworkSecurityGroup(r *resources.Resource) error {
	return g.cloud.NetworkSecurityGroup().Delete(context.TODO(), g.resourceGroupName(), r.Name)
}

func (g *resourceGetter) listApplicationSecurityGroups(ctx context.Context) ([]*resources.Resource, error) {
	ApplicationSecurityGroups, err := g.cloud.ApplicationSecurityGroup().List(ctx, g.resourceGroupName())
	if err != nil {
		return nil, err
	}

	var rs []*resources.Resource
	for _, asg := range ApplicationSecurityGroups {
		rs = append(rs, g.toApplicationSecurityGroupResource(asg))
	}
	return rs, nil
}

func (g *resourceGetter) toApplicationSecurityGroupResource(ApplicationSecurityGroup *network.ApplicationSecurityGroup) *resources.Resource {
	return &resources.Resource{
		Obj:  ApplicationSecurityGroup,
		Type: typeApplicationSecurityGroup,
		ID:   *ApplicationSecurityGroup.Name,
		Name: *ApplicationSecurityGroup.Name,
		Deleter: func(_ fi.Cloud, r *resources.Resource) error {
			return g.deleteApplicationSecurityGroup(r)
		},
		Blocks: []string{
			toKey(typeResourceGroup, g.resourceGroupName()),
		},
	}
}

func (g *resourceGetter) deleteApplicationSecurityGroup(r *resources.Resource) error {
	return g.cloud.ApplicationSecurityGroup().Delete(context.TODO(), g.resourceGroupName(), r.Name)
}

func (g *resourceGetter) listRouteTables(ctx context.Context) ([]*resources.Resource, error) {
	rts, err := g.cloud.RouteTable().List(ctx, g.resourceGroupName())
	if err != nil {
		return nil, err
	}

	var rs []*resources.Resource
	for _, rt := range rts {
		if !g.isOwnedByCluster(rt.Tags) {
			continue
		}
		rs = append(rs, g.toRouteTableResource(rt))
	}
	return rs, nil
}

func (g *resourceGetter) toRouteTableResource(rt *network.RouteTable) *resources.Resource {
	return &resources.Resource{
		Obj:     rt,
		Type:    typeRouteTable,
		ID:      *rt.Name,
		Name:    *rt.Name,
		Deleter: g.deleteRouteTable,
		Blocks:  []string{toKey(typeResourceGroup, g.resourceGroupName())},
		Shared:  g.clusterInfo.AzureRouteTableShared,
	}
}

func (g *resourceGetter) deleteRouteTable(_ fi.Cloud, r *resources.Resource) error {
	return g.cloud.RouteTable().Delete(context.TODO(), g.resourceGroupName(), r.Name)
}

func (g *resourceGetter) listVMScaleSetsAndRoleAssignments(ctx context.Context) ([]*resources.Resource, error) {
	vmsses, err := g.cloud.VMScaleSet().List(ctx, g.resourceGroupName())
	if err != nil {
		return nil, err
	}

	var rs []*resources.Resource
	principalIDs := map[string]*compute.VirtualMachineScaleSet{}
	for _, vmss := range vmsses {
		if !g.isOwnedByCluster(vmss.Tags) {
			continue
		}

		vms, err := g.cloud.VMScaleSetVM().List(ctx, g.resourceGroupName(), *vmss.Name)
		if err != nil {
			return nil, err
		}

		r, err := g.toVMScaleSetResource(vmss, vms)
		if err != nil {
			return nil, err
		}
		rs = append(rs, r)

		principalIDs[*vmss.Identity.PrincipalID] = vmss
	}

	resourceGroupRAs, err := g.listRoleAssignments(ctx, principalIDs, g.resourceGroupID())
	if err != nil {
		return nil, err
	}
	rs = append(rs, resourceGroupRAs...)

	storageAccountRAs, err := g.listRoleAssignments(ctx, principalIDs, g.storageAccountID())
	if err != nil {
		return nil, err
	}
	rs = append(rs, storageAccountRAs...)

	return rs, nil
}

func (g *resourceGetter) toVMScaleSetResource(vmss *compute.VirtualMachineScaleSet, vms []*compute.VirtualMachineScaleSetVM) (*resources.Resource, error) {
	// Add resources whose deletion is blocked by this VMSS.
	var blocks []string
	blocks = append(blocks, toKey(typeResourceGroup, g.resourceGroupName()))

	vnets := set.New[string]()
	subnets := set.New[string]()
	asgs := set.New[string]()
	lbs := set.New[string]()
	for _, iface := range vmss.Properties.VirtualMachineProfile.NetworkProfile.NetworkInterfaceConfigurations {
		for _, ip := range iface.Properties.IPConfigurations {
			subnetID, err := azure.ParseSubnetID(*ip.Properties.Subnet.ID)
			if err != nil {
				return nil, fmt.Errorf("parsing subnet ID: %w", err)
			}
			vnets.Insert(subnetID.VirtualNetworkName)
			subnets.Insert(subnetID.SubnetName)
			if ip.Properties.ApplicationSecurityGroups != nil {
				for _, asg := range ip.Properties.ApplicationSecurityGroups {
					asgID, err := azure.ParseApplicationSecurityGroupID(*asg.ID)
					if err != nil {
						return nil, fmt.Errorf("parsing application security group ID: %w", err)
					}
					asgs.Insert(asgID.ApplicationSecurityGroupName)
				}
			}
			if ip.Properties.LoadBalancerBackendAddressPools != nil {
				for _, lb := range ip.Properties.LoadBalancerBackendAddressPools {
					lbID, err := azure.ParseLoadBalancerID(*lb.ID)
					if err != nil {
						return nil, fmt.Errorf("parsing load balancer ID: %w", err)
					}
					lbs.Insert(lbID.LoadBalancerName)
				}
			}
		}
	}
	for vnet := range vnets {
		blocks = append(blocks, toKey(typeVirtualNetwork, vnet))
	}
	for subnet := range subnets {
		blocks = append(blocks, toKey(typeSubnet, subnet))
	}
	for asg := range asgs {
		blocks = append(blocks, toKey(typeApplicationSecurityGroup, asg))
	}
	for lb := range lbs {
		blocks = append(blocks, toKey(typeLoadBalancer, lb))
	}

	for _, vm := range vms {
		if disks := vm.Properties.StorageProfile.DataDisks; disks != nil {
			for _, d := range disks {
				blocks = append(blocks, toKey(typeDisk, *d.Name))
			}
		}
	}

	return &resources.Resource{
		Obj:     vmss,
		Type:    typeVMScaleSet,
		ID:      *vmss.Name,
		Name:    *vmss.Name,
		Deleter: g.deleteVMScaleSet,
		Blocks:  blocks,
	}, nil
}

func (g *resourceGetter) deleteVMScaleSet(_ fi.Cloud, r *resources.Resource) error {
	return g.cloud.VMScaleSet().Delete(context.TODO(), g.resourceGroupName(), r.Name)
}

func (g *resourceGetter) listDisks(ctx context.Context) ([]*resources.Resource, error) {
	disks, err := g.cloud.Disk().List(ctx, g.resourceGroupName())
	if err != nil {
		return nil, err
	}

	var rs []*resources.Resource
	for _, disk := range disks {
		if !g.isOwnedByCluster(disk.Tags) {
			continue
		}
		rs = append(rs, g.toDiskResource(disk))
	}
	return rs, nil
}

func (g *resourceGetter) toDiskResource(disk *compute.Disk) *resources.Resource {
	return &resources.Resource{
		Obj:     disk,
		Type:    typeDisk,
		ID:      *disk.Name,
		Name:    *disk.Name,
		Deleter: g.deleteDisk,
		Blocks:  []string{toKey(typeResourceGroup, g.resourceGroupName())},
	}
}

func (g *resourceGetter) deleteDisk(_ fi.Cloud, r *resources.Resource) error {
	return g.cloud.Disk().Delete(context.TODO(), g.resourceGroupName(), r.Name)
}

func (g *resourceGetter) listRoleAssignments(ctx context.Context, principalIDs map[string]*compute.VirtualMachineScaleSet, scope string) ([]*resources.Resource, error) {
	ras, err := g.cloud.RoleAssignment().List(ctx, scope)
	if err != nil {
		return nil, err
	}

	var rs []*resources.Resource
	for _, ra := range ras {
		// Add a Role Assignment to the slice if its principal ID is that of one of the VM Scale Sets.
		if ra.Properties == nil || ra.Properties.PrincipalID == nil {
			continue
		}
		vmss, ok := principalIDs[*ra.Properties.PrincipalID]
		if !ok {
			continue
		}
		rs = append(rs, g.toRoleAssignmentResource(ra, vmss))
	}
	return rs, nil
}

func (g *resourceGetter) toRoleAssignmentResource(ra *authz.RoleAssignment, vmss *compute.VirtualMachineScaleSet) *resources.Resource {
	return &resources.Resource{
		Obj:     ra,
		Type:    typeRoleAssignment,
		ID:      *ra.Name,
		Name:    *ra.Name,
		Deleter: g.deleteRoleAssignment,
		Blocks: []string{
			toKey(typeResourceGroup, g.resourceGroupName()),
			toKey(typeVMScaleSet, *vmss.Name),
		},
	}
}

func (g *resourceGetter) deleteRoleAssignment(_ fi.Cloud, r *resources.Resource) error {
	ra, ok := r.Obj.(*authz.RoleAssignment)
	if !ok {
		return fmt.Errorf("expected RoleAssignment, but got %T", r)
	}
	return g.cloud.RoleAssignment().Delete(context.TODO(), *ra.Properties.Scope, *ra.Name)
}

func (g *resourceGetter) listLoadBalancers(ctx context.Context) ([]*resources.Resource, error) {
	loadBalancers, err := g.cloud.LoadBalancer().List(ctx, g.resourceGroupName())
	if err != nil {
		return nil, err
	}

	var rs []*resources.Resource
	for _, lb := range loadBalancers {
		if !g.isOwnedByCluster(lb.Tags) {
			continue
		}
		r, err := g.toLoadBalancerResource(lb)
		if err != nil {
			return nil, err
		}
		rs = append(rs, r)
	}
	return rs, nil
}

func (g *resourceGetter) toLoadBalancerResource(loadBalancer *network.LoadBalancer) (*resources.Resource, error) {
	var blocks []string
	blocks = append(blocks, toKey(typeResourceGroup, g.resourceGroupName()))

	pips := set.New[string]()
	if loadBalancer.Properties != nil {
		for _, fip := range loadBalancer.Properties.FrontendIPConfigurations {
			if fip.Properties == nil || fip.Properties.PublicIPAddress == nil {
				continue
			}
			pipID, err := azure.ParsePublicIPAddressID(*fip.Properties.PublicIPAddress.ID)
			if err != nil {
				return nil, fmt.Errorf("parsing public IP address ID: %s", err)
			}
			pips.Insert(pipID.PublicIPAddressName)
		}
	}
	for pip := range pips {
		blocks = append(blocks, toKey(typePublicIPAddress, pip))
	}

	return &resources.Resource{
		Obj:     loadBalancer,
		Type:    typeLoadBalancer,
		ID:      *loadBalancer.Name,
		Name:    *loadBalancer.Name,
		Deleter: g.deleteLoadBalancer,
		Blocks:  blocks,
	}, nil
}

func (g *resourceGetter) deleteLoadBalancer(_ fi.Cloud, r *resources.Resource) error {
	return g.cloud.LoadBalancer().Delete(context.TODO(), g.resourceGroupName(), r.Name)
}

func (g *resourceGetter) listPublicIPAddresses(ctx context.Context) ([]*resources.Resource, error) {
	publicIPAddresses, err := g.cloud.PublicIPAddress().List(ctx, g.resourceGroupName())
	if err != nil {
		return nil, err
	}

	var rs []*resources.Resource
	for _, pip := range publicIPAddresses {
		if !g.isOwnedByCluster(pip.Tags) {
			continue
		}
		rs = append(rs, g.toPublicIPAddressResource(pip))
	}
	return rs, nil
}

func (g *resourceGetter) toPublicIPAddressResource(publicIPAddress *network.PublicIPAddress) *resources.Resource {
	return &resources.Resource{
		Obj:     publicIPAddress,
		Type:    typePublicIPAddress,
		ID:      *publicIPAddress.Name,
		Name:    *publicIPAddress.Name,
		Deleter: g.deletePublicIPAddress,
		Blocks:  []string{toKey(typeResourceGroup, g.resourceGroupName())},
	}
}

func (g *resourceGetter) deletePublicIPAddress(_ fi.Cloud, r *resources.Resource) error {
	return g.cloud.PublicIPAddress().Delete(context.TODO(), g.resourceGroupName(), r.Name)
}

func (g *resourceGetter) listNatGateways(ctx context.Context) ([]*resources.Resource, error) {
	natGateways, err := g.cloud.NatGateway().List(ctx, g.resourceGroupName())
	if err != nil {
		return nil, err
	}

	var rs []*resources.Resource
	for _, ngw := range natGateways {
		if !g.isOwnedByCluster(ngw.Tags) {
			continue
		}
		r, err := g.toNatGatewayResource(ngw)
		if err != nil {
			return nil, err
		}
		rs = append(rs, r)
	}
	return rs, nil
}

func (g *resourceGetter) toNatGatewayResource(natGateway *network.NatGateway) (*resources.Resource, error) {
	var blocks []string
	blocks = append(blocks, toKey(typeResourceGroup, g.resourceGroupName()))

	pips := set.New[string]()
	if natGateway.Properties != nil && natGateway.Properties.PublicIPAddresses != nil {
		for _, pip := range natGateway.Properties.PublicIPAddresses {
			pipID, err := azure.ParsePublicIPAddressID(*pip.ID)
			if err != nil {
				return nil, fmt.Errorf("parsing public IP address ID: %s", err)
			}
			pips.Insert(pipID.PublicIPAddressName)
		}
	}
	for pip := range pips {
		blocks = append(blocks, toKey(typePublicIPAddress, pip))
	}

	return &resources.Resource{
		Obj:     natGateway,
		Type:    typeNatGateway,
		ID:      *natGateway.ID,
		Name:    *natGateway.Name,
		Deleter: g.deleteNatGateway,
		Blocks:  blocks,
	}, nil
}

func (g *resourceGetter) deleteNatGateway(_ fi.Cloud, r *resources.Resource) error {
	return g.cloud.NatGateway().Delete(context.TODO(), g.resourceGroupName(), r.Name)
}

// isOwnedByCluster returns true if the resource is owned by the cluster.
func (g *resourceGetter) isOwnedByCluster(tags map[string]*string) bool {
	for k, v := range tags {
		if k == azure.TagClusterName && *v == g.clusterInfo.Name {
			return true
		}
	}
	return false
}

func toKey(rtype, id string) string {
	return rtype + ":" + id
}
