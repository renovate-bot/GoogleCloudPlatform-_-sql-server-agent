//go:build windows
// +build windows

/*
Copyright 2023 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package guestcollector contains modules for collecting guest os information.
package guestcollector

import (
	"context"
	"encoding/json"
	"regexp"
	"time"

	"github.com/StackExchange/wmi"
	"github.com/GoogleCloudPlatform/sql-server-agent/internal/agentstatus"
	"github.com/GoogleCloudPlatform/sql-server-agent/internal"
	"github.com/GoogleCloudPlatform/workloadagentplatform/sharedlibraries/log"
)

// WindowsCollector is the collector for windows system.
type WindowsCollector struct {
	host                     any
	username                 any
	password                 any
	guestRuleWMIMap          map[string]wmiExecutor
	logicalToPhysicalDiskMap map[string]string
	physicalDiskToTypeMap    map[string]string
	usageMetricLogger        agentstatus.AgentStatus
}
type wmiExecutor struct {
	namespace   string
	query       string
	isRule      bool
	runWMIQuery func(wmiConnectionArgs) (string, error)
}

// WMIConnectionArgs takes all required fields to run a WMI query.
type wmiConnectionArgs struct {
	host      any
	username  any
	password  any
	namespace string
	query     string
}

// NewWindowsCollector initializes and returns new WindowsCollector object.
func NewWindowsCollector(host, username, password any, usageMetricLogger agentstatus.AgentStatus) *WindowsCollector {
	c := WindowsCollector{
		host:                     host,
		username:                 username,
		password:                 password,
		guestRuleWMIMap:          map[string]wmiExecutor{},
		logicalToPhysicalDiskMap: map[string]string{},
		physicalDiskToTypeMap:    map[string]string{},
		usageMetricLogger:        usageMetricLogger,
	}
	c.guestRuleWMIMap[internal.PowerProfileSettingRule] = wmiExecutor{
		namespace: `root\cimv2\power`,
		query:     `SELECT elementname FROM win32_powerplan WHERE isactive = true`,
		isRule:    true,
		runWMIQuery: func(connArgs wmiConnectionArgs) (string, error) {
			var result []struct {
				ElementName string
			}
			// https://learn.microsoft.com/en-us/windows/win32/wmisdk/swbemlocator-connectserver
			if err := wmi.Query(connArgs.query, &result, connArgs.host, connArgs.namespace, connArgs.username, connArgs.password); err != nil {
				return "", err
			}
			return result[0].ElementName, nil
		},
	}
	c.guestRuleWMIMap[internal.LogicalDiskToPartition] = wmiExecutor{
		namespace: `root\cimv2`,
		query:     `SELECT antecedent, dependent FROM win32_logicaldisktopartition`,
		runWMIQuery: func(connArgs wmiConnectionArgs) (string, error) {
			var result []struct {
				Antecedent string
				Dependent  string
			}
			if err := wmi.Query(connArgs.query, &result, connArgs.host, connArgs.namespace, connArgs.username, connArgs.password); err != nil {
				return "", err
			}
			// example output:
			// Antecedent: \\[HOSTNAME]\root\cimv2:Win32_DiskPartition.DeviceID="Disk #0, Partition #1"
			// Dependent: \\[HOSTNAME]\root\cimv2:Win32_LogicalDisk.DeviceID="C:"
			for _, v := range result {
				if re := regexp.MustCompile(`\.*\\root\\cimv2:Win32_DiskPartition\.DeviceID=\"Disk #(.*), Partition #.*\"`); re.MatchString(v.Antecedent) {
					disk := re.FindStringSubmatch(v.Antecedent)[1]
					if re = regexp.MustCompile(`\.*\\root\\cimv2:Win32_LogicalDisk\.DeviceID=\"(.*)\"`); re.MatchString(v.Dependent) {
						logicalDisk := re.FindStringSubmatch(v.Dependent)[1]
						c.logicalToPhysicalDiskMap[logicalDisk] = disk
					}
				}
			}
			return "", nil
		},
	}
	c.guestRuleWMIMap[internal.PhysicalDiskToType] = wmiExecutor{
		namespace: `root\microsoft\windows\storage`,
		query:     `SELECT deviceid, friendlyname, size, mediatype FROM msft_physicaldisk`,
		runWMIQuery: func(connArgs wmiConnectionArgs) (string, error) {
			var result []struct {
				DeviceID     string
				FriendlyName string
				Size         int64
				MediaType    int16
			}
			if err := wmi.Query(connArgs.query, &result, connArgs.host, connArgs.namespace, connArgs.username, connArgs.password); err != nil {
				return "", err
			}
			for _, v := range result {
				c.physicalDiskToTypeMap[v.DeviceID] = FriendlyNameToDiskType(v.FriendlyName, v.Size, v.MediaType)
			}
			return "", nil
		},
	}
	c.guestRuleWMIMap[internal.DataDiskAllocationUnitsRule] = wmiExecutor{
		namespace: `root\cimv2`,
		isRule:    true,
		query:     `SELECT caption, blocksize FROM win32_volume`,
		runWMIQuery: func(connArgs wmiConnectionArgs) (string, error) {
			var result []struct {
				BlockSize int64
				Caption   string
			}
			if err := wmi.Query(connArgs.query, &result, connArgs.host, connArgs.namespace, connArgs.username, connArgs.password); err != nil {
				return "", err
			}
			re := regexp.MustCompile(`.*Volume{.*}.*`)
			var r []struct {
				BlockSize int64
				Caption   string
			}
			for _, v := range result {
				if !re.MatchString(v.Caption) {
					r = append(r, v)
				}
			}
			res, err := json.Marshal(r)
			if err != nil {
				return "", err
			}
			return string(res), nil
		},
	}
	c.guestRuleWMIMap[internal.GCBDRAgentRunning] = wmiExecutor{
		namespace: `root\cimv2`,
		isRule:    true,
		query:     `SELECT caption FROM Win32_Process WHERE Name="udsagent.exe"`,
		runWMIQuery: func(connArgs wmiConnectionArgs) (string, error) {
			var result []struct {
				Caption string
			}
			if err := wmi.Query(connArgs.query, &result, connArgs.host, connArgs.namespace, connArgs.username, connArgs.password); err != nil {
				return "", err
			}
			if len(result) == 0 {
				return "false", nil
			}
			return "true", nil
		},
	}
	return &c
}

// LogicalDiskMediaType generates the logicalDrive : mediaType mappings and add the result to details.
func (c *WindowsCollector) logicalDiskMediaType(details *internal.Details) {
	logicalToTypeMap := map[string]string{}
	for key, val := range c.logicalToPhysicalDiskMap {
		v, ok := c.physicalDiskToTypeMap[val]
		if ok {
			logicalToTypeMap[key] = v
		}
	}
	if len(logicalToTypeMap) == 0 {
		details.Fields[0][internal.LocalSSDRule] = "unknown"
		return
	}
	r, err := json.Marshal(logicalToTypeMap)
	if err != nil {
		log.Logger.Error(err)
		c.usageMetricLogger.Error(agentstatus.InvalidJSONFormatError)
	} else {
		details.Fields[0][internal.LocalSSDRule] = string(r)
	}
}

// CollectGuestRules collects all guest rules. The rules are defined in rules.go.
func (c *WindowsCollector) CollectGuestRules(ctx context.Context, timeout time.Duration) internal.Details {
	details := internal.Details{
		Name: "OS",
	}
	fields := map[string]string{}
	for rule, exe := range c.guestRuleWMIMap {
		func() {
			ctxWithTimeout, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			ch := make(chan bool, 1)

			go func() {
				connArgs := wmiConnectionArgs{
					host:     c.host,
					username: c.username,
					password: c.password,
				}
				connArgs.namespace = exe.namespace
				connArgs.query = exe.query
				res, err := exe.runWMIQuery(connArgs)
				if err != nil {
					log.Logger.Error(err)
					c.usageMetricLogger.Error(agentstatus.WMIQueryExecutionError)
					if exe.isRule {
						fields[rule] = "unknown"
					}
					ch <- false
					return
				}
				if exe.isRule {
					fields[rule] = res
				}
				ch <- true
			}()
			select {
			case <-ctxWithTimeout.Done():
				log.Logger.Errorf("Running windows guest rule %s timeout", rule)
				c.usageMetricLogger.Error(agentstatus.WinGuestCollectionTimeout)
			case <-ch:
			}
		}()
	}
	details.Fields = append(details.Fields, fields)
	c.logicalDiskMediaType(&details)
	return details
}

// FriendlyNameToDiskType determines disk type based on name, size, and media type.
func FriendlyNameToDiskType(friendlyName string, size int64, mediaType int16) string {
	if (friendlyName == "nvme_card" || friendlyName == "Google EphemeralDisk") && size%402653184000 == 0 {
		return internal.LocalSSD.String()
	} else if friendlyName == "Google PersistentDisk" && mediaType == 4 {
		return internal.PersistentSSD.String()
	} else {
		return internal.Other.String()
	}
}
