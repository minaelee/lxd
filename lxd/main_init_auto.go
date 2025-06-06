package main

import (
	"errors"
	"fmt"
	"slices"

	"github.com/spf13/cobra"

	"github.com/canonical/lxd/client"
	storageDrivers "github.com/canonical/lxd/lxd/storage/drivers"
	"github.com/canonical/lxd/lxd/util"
	"github.com/canonical/lxd/shared"
	"github.com/canonical/lxd/shared/api"
)

// RunAuto initializes LXD in automatic (non-interactive) mode.
func (c *cmdInit) RunAuto(cmd *cobra.Command, args []string, d lxd.InstanceServer, server *api.Server) (*api.InitPreseed, error) {
	// Quick checks.
	if c.flagStorageBackend != "" && !slices.Contains(storageDrivers.AllDriverNames(), c.flagStorageBackend) {
		return nil, fmt.Errorf("The requested backend '%s' isn't supported by lxd init", c.flagStorageBackend)
	}

	if c.flagStorageBackend != "" && !slices.Contains(util.AvailableStorageDrivers(server.Environment.StorageSupportedDrivers, util.PoolTypeAny), c.flagStorageBackend) {
		return nil, fmt.Errorf("The requested backend '%s' isn't available on your system (missing tools)", c.flagStorageBackend)
	}

	if c.flagStorageBackend == "dir" || c.flagStorageBackend == "" {
		if c.flagStorageLoopSize != -1 || c.flagStorageDevice != "" || c.flagStoragePool != "" {
			return nil, errors.New("None of --storage-pool, --storage-create-device or --storage-create-loop may be used with the 'dir' backend")
		}
	} else {
		if c.flagStorageLoopSize != -1 && c.flagStorageDevice != "" {
			return nil, errors.New("Only one of --storage-create-device or --storage-create-loop can be specified")
		}
	}

	if c.flagNetworkAddress == "" {
		if c.flagNetworkPort != -1 {
			return nil, errors.New("--network-port can't be used without --network-address")
		}
	}

	storagePools, err := d.GetStoragePoolNames()
	if err != nil {
		return nil, fmt.Errorf("Failed to retrieve list of storage pools: %w", err)
	}

	if len(storagePools) > 0 && (c.flagStorageBackend != "" || c.flagStorageDevice != "" || c.flagStorageLoopSize != -1 || c.flagStoragePool != "") {
		return nil, errors.New("Storage has already been configured")
	}

	// Defaults
	if c.flagStorageBackend == "" {
		c.flagStorageBackend = "dir"
	}

	if c.flagNetworkPort == -1 {
		c.flagNetworkPort = shared.HTTPSDefaultPort
	}

	// Fill in the node configuration
	config := api.InitLocalPreseed{}
	config.Config = map[string]any{}

	// Network listening
	if c.flagNetworkAddress != "" {
		config.Config["core.https_address"] = util.CanonicalNetworkAddressFromAddressAndPort(c.flagNetworkAddress, c.flagNetworkPort, shared.HTTPSDefaultPort)
	}

	// Storage configuration
	if len(storagePools) == 0 {
		// Storage pool
		pool := api.StoragePoolsPost{
			Name:   "default",
			Driver: c.flagStorageBackend,
		}

		pool.Config = map[string]string{}

		if c.flagStorageDevice != "" {
			pool.Config["source"] = c.flagStorageDevice
		} else if c.flagStorageLoopSize > 0 {
			pool.Config["size"] = fmt.Sprintf("%dGiB", c.flagStorageLoopSize)
		} else {
			pool.Config["source"] = c.flagStoragePool
		}

		// If using a device or loop, --storage-pool refers to the name of the new pool
		if c.flagStoragePool != "" && (c.flagStorageDevice != "" || c.flagStorageLoopSize != -1) {
			pool.Name = c.flagStoragePool
		}

		config.StoragePools = []api.StoragePoolsPost{pool}

		// Profile entry
		config.Profiles = []api.ProfilesPost{{
			Name: "default",
			ProfilePut: api.ProfilePut{
				Devices: map[string]map[string]string{
					"root": {
						"type": "disk",
						"path": "/",
						"pool": pool.Name,
					},
				},
			},
		}}
	}

	// Network configuration
	networks, err := d.GetNetworks()
	if err != nil {
		return nil, fmt.Errorf("Failed to retrieve list of networks: %w", err)
	}

	// Extract managed networks
	managedNetworks := []api.Network{}
	for _, network := range networks {
		if network.Managed {
			managedNetworks = append(managedNetworks, network)
		}
	}

	// Look for an existing network device in the profile
	defaultProfileNetwork := false
	defaultProfile, _, err := d.GetProfile("default")
	if err == nil {
		for _, dev := range defaultProfile.Devices {
			if dev["type"] == "nic" {
				defaultProfileNetwork = true
				break
			}
		}
	}

	// Define a new network
	if len(managedNetworks) == 0 && !defaultProfileNetwork {
		// Find a new name
		idx := 0
		for {
			if shared.PathExists(fmt.Sprintf("/sys/class/net/lxdbr%d", idx)) {
				idx++
				continue
			}

			break
		}

		// Define the new network
		network := api.InitNetworksProjectPost{}
		network.Name = fmt.Sprintf("lxdbr%d", idx)
		network.Project = api.ProjectDefaultName
		config.Networks = append(config.Networks, network)

		// Add it to the profile
		if config.Profiles == nil {
			config.Profiles = []api.ProfilesPost{{
				Name: "default",
				ProfilePut: api.ProfilePut{
					Devices: map[string]map[string]string{
						"eth0": {
							"type":    "nic",
							"network": network.Name,
							"name":    "eth0",
						},
					},
				},
			}}
		} else {
			config.Profiles[0].Devices["eth0"] = map[string]string{
				"type":    "nic",
				"network": network.Name,
				"name":    "eth0",
			}
		}
	}

	return &api.InitPreseed{Node: config}, nil
}
