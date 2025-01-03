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

package openstack

import (
	"context"
	"fmt"

	"github.com/gophercloud/gophercloud/v2/openstack/dns/v2/recordsets"
	"github.com/gophercloud/gophercloud/v2/openstack/dns/v2/zones"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/kops/util/pkg/vfs"
)

// ListDNSZones will list available DNS zones
func (c *openstackCloud) ListDNSZones(opt zones.ListOptsBuilder) ([]zones.Zone, error) {
	return listDNSZones(c, opt)
}

func listDNSZones(c OpenstackCloud, opt zones.ListOptsBuilder) ([]zones.Zone, error) {
	var zs []zones.Zone

	done, err := vfs.RetryWithBackoff(readBackoff, func() (bool, error) {
		allPages, err := zones.List(c.DNSClient(), opt).AllPages(context.TODO())
		if err != nil {
			return false, fmt.Errorf("failed to list dns zones: %s", err)
		}
		r, err := zones.ExtractZones(allPages)
		if err != nil {
			return false, fmt.Errorf("failed to extract dns zone pages: %s", err)
		}
		zs = r
		return true, nil
	})
	if err != nil {
		return zs, err
	} else if done {
		return zs, nil
	} else {
		return zs, wait.ErrWaitTimeout
	}
}

func deleteDNSRecordset(c OpenstackCloud, zoneID string, rrsetID string) error {
	done, err := vfs.RetryWithBackoff(writeBackoff, func() (bool, error) {
		err := recordsets.Delete(context.TODO(), c.DNSClient(), zoneID, rrsetID).ExtractErr()
		if err != nil {
			return false, fmt.Errorf("failed to delete dns recordset: %s", err)
		}
		return true, nil
	})
	if err != nil {
		return err
	} else if done {
		return nil
	} else {
		return wait.ErrWaitTimeout
	}
}

// DeleteDNSRecordset will delete single DNS recordset in zone
func (c *openstackCloud) DeleteDNSRecordset(zoneID string, rrsetID string) error {
	return deleteDNSRecordset(c, zoneID, rrsetID)
}

// ListDNSRecordsets will list DNS recordsets
func (c *openstackCloud) ListDNSRecordsets(zoneID string, opt recordsets.ListOptsBuilder) ([]recordsets.RecordSet, error) {
	return listDNSRecordsets(c, zoneID, opt)
}

func listDNSRecordsets(c OpenstackCloud, zoneID string, opt recordsets.ListOptsBuilder) ([]recordsets.RecordSet, error) {
	var rrs []recordsets.RecordSet

	done, err := vfs.RetryWithBackoff(readBackoff, func() (bool, error) {
		allPages, err := recordsets.ListByZone(c.DNSClient(), zoneID, opt).AllPages(context.TODO())
		if err != nil {
			return false, fmt.Errorf("failed to list dns recordsets: %s", err)
		}
		r, err := recordsets.ExtractRecordSets(allPages)
		if err != nil {
			return false, fmt.Errorf("failed to extract dns recordsets pages: %s", err)
		}
		rrs = r
		return true, nil
	})
	if err != nil {
		return rrs, err
	} else if done {
		return rrs, nil
	} else {
		return rrs, wait.ErrWaitTimeout
	}
}
