package main

import (
	"fmt"
	"maps"
	"slices"

	"github.com/canonical/lxd/client"
	"github.com/canonical/lxd/shared"
	"github.com/canonical/lxd/shared/api"
	"github.com/canonical/lxd/shared/revert"
)

// Helper to initialize node-specific entities on a LXD instance using the
// definitions from the given api.InitLocalPreseed object.
//
// It's used both by the 'lxd init' command and by the PUT /1.0/cluster API.
//
// In case of error, the returned function can be used to revert the changes.
func initDataNodeApply(d lxd.InstanceServer, config api.InitLocalPreseed) (func(), error) {
	revert := revert.New()
	defer revert.Fail()

	// Apply server configuration.
	if len(config.Config) > 0 {
		// Get current config.
		currentServer, etag, err := d.GetServer()
		if err != nil {
			return nil, fmt.Errorf("Failed to retrieve current server configuration: %w", err)
		}

		// Setup reverter.
		revert.Add(func() { _ = d.UpdateServer(currentServer.Writable(), "") })

		// Prepare the update.
		newServer := api.ServerPut{}
		err = shared.DeepCopy(currentServer.Writable(), &newServer)
		if err != nil {
			return nil, fmt.Errorf("Failed to copy server configuration: %w", err)
		}

		for k, v := range config.Config {
			newServer.Config[k] = fmt.Sprint(v)
		}

		// Apply it.
		err = d.UpdateServer(newServer, etag)
		if err != nil {
			return nil, fmt.Errorf("Failed to update server configuration: %w", err)
		}
	}

	// Apply storage configuration.
	if len(config.StoragePools) > 0 {
		// Get the list of storagePools.
		storagePoolNames, err := d.GetStoragePoolNames()
		if err != nil {
			return nil, fmt.Errorf("Failed to retrieve list of storage pools: %w", err)
		}

		// StoragePool creator
		createStoragePool := func(storagePool api.StoragePoolsPost) error {
			// Create the storagePool if doesn't exist.
			err := d.CreateStoragePool(storagePool)
			if err != nil {
				return fmt.Errorf("Failed to create storage pool %q: %w", storagePool.Name, err)
			}

			// Setup reverter.
			revert.Add(func() { _ = d.DeleteStoragePool(storagePool.Name) })
			return nil
		}

		// StoragePool updater.
		updateStoragePool := func(storagePool api.StoragePoolsPost) error {
			// Get the current storagePool.
			currentStoragePool, etag, err := d.GetStoragePool(storagePool.Name)
			if err != nil {
				return fmt.Errorf("Failed to retrieve current storage pool %q: %w", storagePool.Name, err)
			}

			// Quick check.
			if currentStoragePool.Driver != storagePool.Driver {
				return fmt.Errorf("Storage pool %q is of type %q instead of %q", currentStoragePool.Name, currentStoragePool.Driver, storagePool.Driver)
			}

			// Setup reverter.
			revert.Add(func() { _ = d.UpdateStoragePool(currentStoragePool.Name, currentStoragePool.Writable(), "") })

			// Prepare the update.
			newStoragePool := api.StoragePoolPut{}
			err = shared.DeepCopy(currentStoragePool.Writable(), &newStoragePool)
			if err != nil {
				return fmt.Errorf("Failed to copy configuration of storage pool %q: %w", storagePool.Name, err)
			}

			// Description override.
			if storagePool.Description != "" {
				newStoragePool.Description = storagePool.Description
			}

			// Config overrides.
			maps.Copy(newStoragePool.Config, storagePool.Config)

			// Apply it.
			err = d.UpdateStoragePool(currentStoragePool.Name, newStoragePool, etag)
			if err != nil {
				return fmt.Errorf("Failed to update storage pool %q: %w", storagePool.Name, err)
			}

			return nil
		}

		for _, storagePool := range config.StoragePools {
			// New storagePool.
			if !slices.Contains(storagePoolNames, storagePool.Name) {
				err := createStoragePool(storagePool)
				if err != nil {
					return nil, err
				}

				continue
			}

			// Existing storagePool.
			err := updateStoragePool(storagePool)
			if err != nil {
				return nil, err
			}
		}
	}

	// Apply network configuration function.
	applyNetwork := func(network api.InitNetworksProjectPost) error {
		currentNetwork, etag, err := d.UseProject(network.Project).GetNetwork(network.Name)
		if err != nil {
			// Create the network if doesn't exist.
			err := d.UseProject(network.Project).CreateNetwork(network.NetworksPost)
			if err != nil {
				return fmt.Errorf("Failed to create local member network %q in project %q: %w", network.Name, network.Project, err)
			}

			// Setup reverter.
			revert.Add(func() { _ = d.UseProject(network.Project).DeleteNetwork(network.Name) })
		} else {
			// Prepare the update.
			newNetwork := api.NetworkPut{}
			err = shared.DeepCopy(currentNetwork.Writable(), &newNetwork)
			if err != nil {
				return fmt.Errorf("Failed to copy configuration of network %q in project %q: %w", network.Name, network.Project, err)
			}

			// Description override.
			if network.Description != "" {
				newNetwork.Description = network.Description
			}

			// Config overrides.
			maps.Copy(newNetwork.Config, network.Config)

			// Apply it.
			err = d.UseProject(network.Project).UpdateNetwork(currentNetwork.Name, newNetwork, etag)
			if err != nil {
				return fmt.Errorf("Failed to update local member network %q in project %q: %w", network.Name, network.Project, err)
			}

			// Setup reverter.
			revert.Add(func() {
				_ = d.UseProject(network.Project).UpdateNetwork(currentNetwork.Name, currentNetwork.Writable(), "")
			})
		}

		return nil
	}

	// Apply networks in the default project before other projects config applied (so that if the projects
	// depend on a network in the default project they can have their config applied successfully).
	for i := range config.Networks {
		// Populate default project if not specified for backwards compatbility with earlier
		// preseed dump files.
		if config.Networks[i].Project == "" {
			config.Networks[i].Project = api.ProjectDefaultName
		}

		if config.Networks[i].Project != api.ProjectDefaultName {
			continue
		}

		err := applyNetwork(config.Networks[i])
		if err != nil {
			return nil, err
		}
	}

	// Apply project configuration.
	if len(config.Projects) > 0 {
		// Get the list of projects.
		projectNames, err := d.GetProjectNames()
		if err != nil {
			return nil, fmt.Errorf("Failed to retrieve list of projects: %w", err)
		}

		// Project creator.
		createProject := func(project api.ProjectsPost) error {
			// Create the project if doesn't exist.
			err := d.CreateProject(project)
			if err != nil {
				return fmt.Errorf("Failed to create local member project %q: %w", project.Name, err)
			}

			// Setup reverter.
			revert.Add(func() { _ = d.DeleteProject(project.Name) })
			return nil
		}

		// Project updater.
		updateProject := func(project api.ProjectsPost) error {
			// Get the current project.
			currentProject, etag, err := d.GetProject(project.Name)
			if err != nil {
				return fmt.Errorf("Failed to retrieve current project %q: %w", project.Name, err)
			}

			// Setup reverter.
			revert.Add(func() { _ = d.UpdateProject(currentProject.Name, currentProject.Writable(), "") })

			// Prepare the update.
			newProject := api.ProjectPut{}
			err = shared.DeepCopy(currentProject.Writable(), &newProject)
			if err != nil {
				return fmt.Errorf("Failed to copy configuration of project %q: %w", project.Name, err)
			}

			// Description override.
			if project.Description != "" {
				newProject.Description = project.Description
			}

			// Config overrides.
			maps.Copy(newProject.Config, project.Config)

			// Apply it.
			err = d.UpdateProject(currentProject.Name, newProject, etag)
			if err != nil {
				return fmt.Errorf("Failed to update local member project %q: %w", project.Name, err)
			}

			return nil
		}

		for _, project := range config.Projects {
			// New project.
			if !slices.Contains(projectNames, project.Name) {
				err := createProject(project)
				if err != nil {
					return nil, err
				}

				continue
			}

			// Existing project.
			err := updateProject(project)
			if err != nil {
				return nil, err
			}
		}
	}

	// Apply networks in non-default projects after project config applied (so that their projects exist).
	for i := range config.Networks {
		if config.Networks[i].Project == api.ProjectDefaultName {
			continue
		}

		err := applyNetwork(config.Networks[i])
		if err != nil {
			return nil, err
		}
	}

	// Apply storage volumes configuration.
	applyStorageVolume := func(storageVolume api.InitStorageVolumesProjectPost) error {
		// Get the current storageVolume.
		currentStorageVolume, etag, err := d.UseProject(storageVolume.Project).GetStoragePoolVolume(storageVolume.Pool, storageVolume.Type, storageVolume.Name)

		if err != nil {
			// Create the storage volume if it doesn't exist.
			err := d.UseProject(storageVolume.Project).CreateStoragePoolVolume(storageVolume.Pool, storageVolume.StorageVolumesPost)
			if err != nil {
				return fmt.Errorf("Failed to create storage volume %q in project %q on pool %q: %w", storageVolume.Name, storageVolume.Project, storageVolume.Pool, err)
			}

			// Setup reverter.
			revert.Add(func() {
				_ = d.UseProject(storageVolume.Project).DeleteStoragePoolVolume(storageVolume.Pool, storageVolume.Type, storageVolume.Name)
			})
		} else {
			// Quick check.
			if currentStorageVolume.Type != storageVolume.Type {
				return fmt.Errorf("Storage volume %q in project %q is of type %q instead of %q", currentStorageVolume.Name, storageVolume.Project, currentStorageVolume.Type, storageVolume.Type)
			}

			// Setup reverter.
			revert.Add(func() {
				_ = d.UseProject(storageVolume.Project).UpdateStoragePoolVolume(storageVolume.Pool, currentStorageVolume.Type, currentStorageVolume.Name, currentStorageVolume.Writable(), "")
			})

			// Prepare the update.
			newStorageVolume := api.StorageVolumePut{}
			err = shared.DeepCopy(currentStorageVolume.Writable(), &newStorageVolume)
			if err != nil {
				return fmt.Errorf("Failed to copy configuration of storage volume %q in project %q: %w", storageVolume.Name, storageVolume.Project, err)
			}

			// Description override.
			if storageVolume.Description != "" {
				newStorageVolume.Description = storageVolume.Description
			}

			// Config overrides.
			maps.Copy(newStorageVolume.Config, storageVolume.Config)

			// Apply it.
			err = d.UseProject(storageVolume.Project).UpdateStoragePoolVolume(storageVolume.Pool, storageVolume.Type, currentStorageVolume.Name, newStorageVolume, etag)
			if err != nil {
				return fmt.Errorf("Failed to update storage volume %q in project %q: %w", storageVolume.Name, storageVolume.Project, err)
			}
		}

		return nil
	}

	// Apply storage volumes in the default project before other projects config.
	for i := range config.StorageVolumes {
		// Populate default project if not specified.
		if config.StorageVolumes[i].Project == "" {
			config.StorageVolumes[i].Project = api.ProjectDefaultName
		}
		// Populate default type if not specified.
		if config.StorageVolumes[i].Type == "" {
			config.StorageVolumes[i].Type = "custom"
		}

		err := applyStorageVolume(config.StorageVolumes[i])
		if err != nil {
			return nil, err
		}
	}

	// Apply profile configuration.
	if len(config.Profiles) > 0 {
		// Get the list of profiles.
		profileNames, err := d.GetProfileNames()
		if err != nil {
			return nil, fmt.Errorf("Failed to retrieve list of profiles: %w", err)
		}

		// Profile creator.
		createProfile := func(profile api.ProfilesPost) error {
			// Create the profile if doesn't exist.
			err := d.CreateProfile(profile)
			if err != nil {
				return fmt.Errorf("Failed to create profile %q: %w", profile.Name, err)
			}

			// Setup reverter.
			revert.Add(func() { _ = d.DeleteProfile(profile.Name) })
			return nil
		}

		// Profile updater.
		updateProfile := func(profile api.ProfilesPost) error {
			// Get the current profile.
			currentProfile, etag, err := d.GetProfile(profile.Name)
			if err != nil {
				return fmt.Errorf("Failed to retrieve current profile %q: %w", profile.Name, err)
			}

			// Setup reverter.
			revert.Add(func() { _ = d.UpdateProfile(currentProfile.Name, currentProfile.Writable(), "") })

			// Prepare the update.
			newProfile := api.ProfilePut{}
			err = shared.DeepCopy(currentProfile.Writable(), &newProfile)
			if err != nil {
				return fmt.Errorf("Failed to copy configuration of profile %q: %w", profile.Name, err)
			}

			// Description override.
			if profile.Description != "" {
				newProfile.Description = profile.Description
			}

			// Config overrides.
			maps.Copy(newProfile.Config, profile.Config)

			// Device overrides.
			for k, v := range profile.Devices {
				// New device.
				_, ok := newProfile.Devices[k]
				if !ok {
					newProfile.Devices[k] = v
					continue
				}

				// Existing device.
				maps.Copy(newProfile.Devices[k], v)
			}

			// Apply it.
			err = d.UpdateProfile(currentProfile.Name, newProfile, etag)
			if err != nil {
				return fmt.Errorf("Failed to update profile %q: %w", profile.Name, err)
			}

			return nil
		}

		for _, profile := range config.Profiles {
			// New profile.
			if !slices.Contains(profileNames, profile.Name) {
				err := createProfile(profile)
				if err != nil {
					return nil, err
				}

				continue
			}

			// Existing profile.
			err := updateProfile(profile)
			if err != nil {
				return nil, err
			}
		}
	}

	cleanup := revert.Clone().Fail // Clone before calling revert.Success() so we can return the Fail func.
	revert.Success()
	return cleanup, nil
}

// Helper to initialize LXD clustering.
//
// Used by the 'lxd init' command.
func initDataClusterApply(d lxd.InstanceServer, config *api.InitClusterPreseed) error {
	if config == nil || !config.Enabled {
		return nil
	}

	// Get the current cluster configuration
	currentCluster, etag, err := d.GetCluster()
	if err != nil {
		return fmt.Errorf("Failed to retrieve current cluster config: %w", err)
	}

	// Check if already enabled
	if !currentCluster.Enabled {
		// Configure the cluster
		op, err := d.UpdateCluster(config.ClusterPut, etag)
		if err != nil {
			return fmt.Errorf("Failed to configure cluster: %w", err)
		}

		err = op.Wait()
		if err != nil {
			return fmt.Errorf("Failed to configure cluster: %w", err)
		}
	}

	return nil
}
