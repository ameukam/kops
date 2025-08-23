/*
Copyright 2019 The Kubernetes Authors.

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

package dotasks

import (
	"context"
	"errors"
	"fmt"

	"github.com/digitalocean/godo"

	"k8s.io/kops/upup/pkg/fi"
	"k8s.io/kops/upup/pkg/fi/cloudup/do"
	"k8s.io/kops/upup/pkg/fi/cloudup/terraform"
	"k8s.io/kops/upup/pkg/fi/cloudup/terraformWriter"
)

// Droplet represents a group of droplets. In the future it
// will be managed by the Machines API
// +kops:fitask
type Droplet struct {
	Name      *string
	Lifecycle fi.Lifecycle

	Region      *string
	Size        *string
	Image       *string
	SSHKey      *SSHKey
	VPCUUID     *string
	NetworkCIDR *string
	VPCName     *string
	Tags        []string
	Count       int
	UserData    fi.Resource
}

var (
	_ fi.CloudupTask   = &Droplet{}
	_ fi.CompareWithID = &Droplet{}
)

func (d *Droplet) CompareWithID() *string {
	return d.Name
}

func (d *Droplet) Find(c *fi.CloudupContext) (*Droplet, error) {
	cloud := c.T.Cloud.(do.DOCloud)

	droplets, err := listDroplets(cloud)
	if err != nil {
		return nil, err
	}

	found := false
	count := 0
	var foundDroplet godo.Droplet
	for _, droplet := range droplets {
		if droplet.Name == fi.ValueOf(d.Name) {
			found = true
			count++
			foundDroplet = droplet
		}
	}

	if !found {
		return nil, nil
	}

	return &Droplet{
		Name:      fi.PtrTo(foundDroplet.Name),
		Count:     count,
		Region:    fi.PtrTo(foundDroplet.Region.Slug),
		Size:      fi.PtrTo(foundDroplet.Size.Slug),
		Image:     d.Image, //Image should not change so we keep it as-is
		Tags:      foundDroplet.Tags,
		SSHKey:    d.SSHKey,   // TODO: get from droplet or ignore change
		UserData:  d.UserData, // TODO: get from droplet or ignore change
		VPCUUID:   fi.PtrTo(foundDroplet.VPCUUID),
		Lifecycle: d.Lifecycle,
	}, nil
}

func listDroplets(cloud do.DOCloud) ([]godo.Droplet, error) {
	allDroplets := []godo.Droplet{}

	opt := &godo.ListOptions{}
	for {
		droplets, resp, err := cloud.DropletsService().List(context.TODO(), opt)
		if err != nil {
			return nil, err
		}

		allDroplets = append(allDroplets, droplets...)

		if resp.Links == nil || resp.Links.IsLastPage() {
			break
		}

		page, err := resp.Links.CurrentPage()
		if err != nil {
			return nil, err
		}

		opt.Page = page + 1
	}

	return allDroplets, nil
}

func (d *Droplet) Run(c *fi.CloudupContext) error {
	return fi.CloudupDefaultDeltaRunMethod(d, c)
}

func (_ *Droplet) RenderDO(t *do.DOAPITarget, a, e, changes *Droplet) error {
	ctx := context.TODO()

	userData, err := fi.ResourceAsString(e.UserData)
	if err != nil {
		return err
	}

	var newDropletCount int
	if a == nil {
		newDropletCount = e.Count
	} else {

		expectedCount := e.Count
		actualCount := a.Count

		if expectedCount == actualCount {
			return nil
		}

		if actualCount > expectedCount {
			return errors.New("deleting droplets is not supported yet")
		}

		newDropletCount = expectedCount - actualCount
	}

	// associate vpcuuid to the droplet if set.
	vpcUUID := ""
	if fi.ValueOf(e.NetworkCIDR) != "" {
		s, err := t.Cloud.GetVPCUUID(fi.ValueOf(e.NetworkCIDR), fi.ValueOf(e.VPCName))
		if err != nil {
			return fmt.Errorf("fetching vpcUUID from network cidr=%s: %w", fi.ValueOf(e.NetworkCIDR), err)
		}
		vpcUUID = s
	} else if fi.ValueOf(e.VPCUUID) != "" {
		vpcUUID = fi.ValueOf(e.VPCUUID)
	}

	for i := 0; i < newDropletCount; i++ {
		req := &godo.DropletCreateRequest{
			Name:     fi.ValueOf(e.Name),
			Region:   fi.ValueOf(e.Region),
			Size:     fi.ValueOf(e.Size),
			Image:    godo.DropletCreateImage{Slug: fi.ValueOf(e.Image)},
			Tags:     e.Tags,
			VPCUUID:  vpcUUID,
			UserData: userData,
		}

		if e.SSHKey != nil {
			req.SSHKeys = append(req.SSHKeys, godo.DropletCreateSSHKey{
				ID: *e.SSHKey.ID,
			})
		}

		_, _, err := t.Cloud.DropletsService().Create(ctx, req)
		if err != nil {
			return fmt.Errorf("error creating droplet with name %q: %w", fi.ValueOf(e.Name), err)
		}
	}

	return nil
}

func (_ *Droplet) CheckChanges(a, e, changes *Droplet) error {
	if a != nil {
		if changes.Name != nil {
			return fi.CannotChangeField("Name")
		}
		if changes.Region != nil {
			return fi.CannotChangeField("Region")
		}
		if changes.Size != nil {
			return fi.CannotChangeField("Size")
		}
		if changes.Image != nil {
			return fi.CannotChangeField("Image")
		}
	} else {
		if e.Name == nil {
			return fi.RequiredField("Name")
		}
		if e.Region == nil {
			return fi.RequiredField("Region")
		}
		if e.Size == nil {
			return fi.RequiredField("Size")
		}
		if e.Image == nil {
			return fi.RequiredField("Image")
		}
	}
	return nil
}

type terraformDropletOptions struct {
	Image    *string                  `cty:"image"`
	Size     *string                  `cty:"size"`
	Region   *string                  `cty:"region"`
	Name     *string                  `cty:"name"`
	Tags     []string                 `cty:"tags"`
	SSHKey   []string                 `cty:"ssh_keys"`
	UserData *terraformWriter.Literal `cty:"user_data"`
	VPCUUID  *string                  `cty:"vpc_uuid"`
}

func (_ *Droplet) RenderTerraform(t *terraform.TerraformTarget, a, e, changes *Droplet) error {
	tf := &terraformDropletOptions{
		Image:   e.Image,
		Size:    e.Size,
		Region:  e.Region,
		Name:    e.Name,
		Tags:    e.Tags,
		VPCUUID: e.VPCUUID,
	}

	if e.SSHKey != nil {
		tf.SSHKey = []string{fi.ValueOf(e.SSHKey.KeyFingerprint)}
	}

	if e.UserData != nil {
		d, err := fi.ResourceAsBytes(e.UserData)
		if err != nil {
			return fmt.Errorf("Error retrieving droplet from resource bytes: %w", err)
		}
		if d != nil {
			tf.UserData, err = t.AddFileBytes("digitalocean_droplet", fi.ValueOf(e.Name), "user_data", d, false)
			if err != nil {
				return fmt.Errorf("Error adding user data bytes to terraform resource: %w", err)
			}
		}
	}

	return t.RenderResource("digitalocean_droplet", *e.Name, tf)
}
