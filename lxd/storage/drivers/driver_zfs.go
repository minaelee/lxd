package drivers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/canonical/lxd/lxd/migration"
	"github.com/canonical/lxd/lxd/operations"
	"github.com/canonical/lxd/lxd/util"
	"github.com/canonical/lxd/shared"
	"github.com/canonical/lxd/shared/api"
	"github.com/canonical/lxd/shared/logger"
	"github.com/canonical/lxd/shared/revert"
	"github.com/canonical/lxd/shared/units"
	"github.com/canonical/lxd/shared/validate"
	"github.com/canonical/lxd/shared/version"
)

var zfsVersion string
var zfsLoaded bool
var zfsDirectIO bool
var zfsTrim bool
var zfsRaw bool
var zfsDelegate bool

var zfsDefaultSettings = map[string]string{
	"relatime":   "on",
	"mountpoint": "legacy",
	"setuid":     "on",
	"exec":       "on",
	"devices":    "on",
	"acltype":    "posixacl",
	"xattr":      "sa",
}

type zfs struct {
	common
}

// load is used to run one-time action per-driver rather than per-pool.
func (d *zfs) load() error {
	// Register the patches.
	d.patches = map[string]func() error{
		"storage_lvm_skipactivation":                         nil,
		"storage_missing_snapshot_records":                   nil,
		"storage_delete_old_snapshot_records":                nil,
		"storage_zfs_drop_block_volume_filesystem_extension": d.patchDropBlockVolumeFilesystemExtension,
		"storage_prefix_bucket_names_with_project":           nil,
	}

	// Done if previously loaded.
	if zfsLoaded {
		return nil
	}

	// Load the kernel module.
	err := util.LoadModule("zfs")
	if err != nil {
		return fmt.Errorf("Error loading %q module: %w", "zfs", err)
	}

	// Get the version information.
	if zfsVersion == "" {
		version, err := d.version()
		if err != nil {
			return err
		}

		zfsVersion = version
	}

	// Validate the needed tools are present.
	for _, tool := range []string{"zpool", "zfs"} {
		_, err := exec.LookPath(tool)
		if err != nil {
			if shared.InSnap() {
				return fmt.Errorf("Required tool %q is missing. The snap does not contain ZFS tools matching the module version (%q). Consider installing ZFS tools in the host and use 'snap set lxd zfs.external=true'", tool, zfsVersion)
			}

			return fmt.Errorf("Required tool %q is missing", tool)
		}
	}

	// Decide whether we can use features added by 0.8.0.
	ver080, err := version.Parse("0.8.0")
	if err != nil {
		return err
	}

	ourVer, err := version.Parse(zfsVersion)
	if err != nil {
		return err
	}

	// If running 0.8.0 or newer, we can use direct I/O, trim and raw.
	if ourVer.Compare(ver080) >= 0 {
		zfsDirectIO = true
		zfsTrim = true
		zfsRaw = true
	}

	// Detect support for ZFS delegation.
	ver220, err := version.Parse("2.2.0")
	if err != nil {
		return err
	}

	if ourVer.Compare(ver220) >= 0 {
		zfsDelegate = true
	}

	zfsLoaded = true
	return nil
}

// Info returns info about the driver and its environment.
func (d *zfs) Info() Info {
	info := Info{
		Name:                         "zfs",
		Version:                      zfsVersion,
		DefaultBlockSize:             d.defaultBlockVolumeSize(),
		DefaultVMBlockFilesystemSize: d.defaultVMBlockFilesystemSize(),
		OptimizedImages:              true,
		OptimizedBackups:             true,
		PreservesInodes:              true,
		Remote:                       d.isRemote(),
		VolumeTypes:                  []VolumeType{VolumeTypeBucket, VolumeTypeCustom, VolumeTypeImage, VolumeTypeContainer, VolumeTypeVM},
		BlockBacking:                 shared.IsTrue(d.config["volume.zfs.block_mode"]),
		RunningCopyFreeze:            false,
		DirectIO:                     zfsDirectIO,
		MountedRoot:                  false,
		Buckets:                      true,
		PopulateParentVolumeUUID:     false,
	}

	return info
}

// ensureInitialDatasets creates missing initial datasets or configures existing ones with current policy.
// Accepts warnOnExistingPolicyApplyError argument, if true will warn rather than fail if applying current policy
// to an existing dataset fails.
func (d zfs) ensureInitialDatasets(warnOnExistingPolicyApplyError bool) error {
	properties := make([]string, 0, len(zfsDefaultSettings))
	for k, v := range zfsDefaultSettings {
		properties = append(properties, k+"="+v)
	}

	properties, err := d.filterRedundantOptions(d.config["zfs.pool_name"], properties...)
	if err != nil {
		return err
	}

	if len(properties) > 0 {
		err := d.setDatasetProperties(d.config["zfs.pool_name"], properties...)
		if err != nil {
			if !warnOnExistingPolicyApplyError {
				return fmt.Errorf("Failed applying policy to existing dataset %q: %w", d.config["zfs.pool_name"], err)
			}

			d.logger.Warn("Failed applying policy to existing dataset", logger.Ctx{"dataset": d.config["zfs.pool_name"], "err": err})
		}
	}

	for _, dataset := range d.initialDatasets() {
		properties := []string{"mountpoint=legacy"}
		if slices.Contains([]string{"virtual-machines", "deleted/virtual-machines"}, dataset) {
			properties = append(properties, "volmode=none")
		}

		datasetPath := d.config["zfs.pool_name"] + "/" + dataset
		exists, err := d.datasetExists(datasetPath)
		if err != nil {
			return err
		}

		if exists {
			properties, err = d.filterRedundantOptions(datasetPath, properties...)
			if err != nil {
				return err
			}

			if len(properties) > 0 {
				err = d.setDatasetProperties(datasetPath, properties...)
				if err != nil {
					if !warnOnExistingPolicyApplyError {
						return fmt.Errorf("Failed applying policy to existing dataset %q: %w", datasetPath, err)
					}

					d.logger.Warn("Failed applying policy to existing dataset", logger.Ctx{"dataset": datasetPath, "err": err})
				}
			}
		} else {
			err = d.createDataset(datasetPath, properties...)
			if err != nil {
				return fmt.Errorf("Failed creating dataset %q: %w", datasetPath, err)
			}
		}
	}

	return nil
}

// FillConfig populates the storage pool's configuration file with the default values.
func (d *zfs) FillConfig() error {
	loopPath := loopFilePath(d.name)
	if d.config["source"] == "" || d.config["source"] == loopPath {
		// Create a loop based pool.
		d.config["source"] = loopPath

		// Set default pool_name.
		if d.config["zfs.pool_name"] == "" {
			d.config["zfs.pool_name"] = d.name
		}

		// Pick a default size of the loop file if not specified.
		if d.config["size"] == "" {
			defaultSize, err := loopFileSizeDefault()
			if err != nil {
				return err
			}

			d.config["size"] = strconv.FormatUint(defaultSize, 10) + "GiB"
		}
	} else if filepath.IsAbs(d.config["source"]) {
		// Set default pool_name.
		if d.config["zfs.pool_name"] == "" {
			d.config["zfs.pool_name"] = d.name
		}

		// Unset size property since it's irrelevant.
		d.config["size"] = ""
	} else {
		// Handle an existing zpool.
		if d.config["zfs.pool_name"] == "" {
			d.config["zfs.pool_name"] = d.config["source"]
		}

		// Unset size property since it's irrelevant.
		d.config["size"] = ""
	}

	return nil
}

// Create is called during pool creation and is effectively using an empty driver struct.
// WARNING: The Create() function cannot rely on any of the struct attributes being set.
func (d *zfs) Create() error {
	// Store the provided source as we are likely to be mangling it.
	d.config["volatile.initial_source"] = d.config["source"]

	revert := revert.New()
	defer revert.Fail()

	err := d.FillConfig()
	if err != nil {
		return err
	}

	loopPath := loopFilePath(d.name)
	if d.config["source"] == "" || d.config["source"] == loopPath {
		// Validate pool_name.
		if strings.Contains(d.config["zfs.pool_name"], "/") {
			return errors.New("zfs.pool_name can't point to a dataset when source isn't set")
		}

		// Create the loop file itself.
		size, err := units.ParseByteSizeString(d.config["size"])
		if err != nil {
			return err
		}

		err = ensureSparseFile(loopPath, size)
		if err != nil {
			return err
		}

		revert.Add(func() { _ = os.Remove(d.config["source"]) })

		// Create the zpool.
		_, err = shared.RunCommandContext(d.state.ShutdownCtx, "zpool", "create", "-m", "none", "-O", "compression=on", d.config["zfs.pool_name"], loopPath)
		if err != nil {
			return err
		}

		// Apply auto-trim if supported.
		if zfsTrim {
			_, err := shared.RunCommandContext(d.state.ShutdownCtx, "zpool", "set", "autotrim=on", d.config["zfs.pool_name"])
			if err != nil {
				return err
			}
		}
	} else if filepath.IsAbs(d.config["source"]) {
		// Handle existing block devices.
		if !shared.IsBlockdevPath(d.config["source"]) {
			return errors.New("Custom loop file locations are not supported")
		}

		// Validate pool_name.
		if strings.Contains(d.config["zfs.pool_name"], "/") {
			return errors.New("zfs.pool_name can't point to a dataset when source isn't set")
		}

		// Wipe if requested.
		if shared.IsTrue(d.config["source.wipe"]) {
			err := wipeBlockHeaders(d.config["source"])
			if err != nil {
				return fmt.Errorf("Failed to wipe headers from disk %q: %w", d.config["source"], err)
			}

			d.config["source.wipe"] = ""

			// Create the zpool.
			_, err = shared.RunCommandContext(d.state.ShutdownCtx, "zpool", "create", "-f", "-m", "none", "-O", "compression=on", d.config["zfs.pool_name"], d.config["source"])
			if err != nil {
				return err
			}
		} else {
			// Create the zpool.
			_, err := shared.RunCommandContext(d.state.ShutdownCtx, "zpool", "create", "-m", "none", "-O", "compression=on", d.config["zfs.pool_name"], d.config["source"])
			if err != nil {
				return err
			}
		}

		// Apply auto-trim if supported.
		if zfsTrim {
			_, err := shared.RunCommandContext(d.state.ShutdownCtx, "zpool", "set", "autotrim=on", d.config["zfs.pool_name"])
			if err != nil {
				return err
			}
		}

		// We don't need to keep the original source path around for import.
		d.config["source"] = d.config["zfs.pool_name"]
	} else {
		// Validate pool_name.
		if d.config["zfs.pool_name"] != d.config["source"] {
			return errors.New("The source must match zfs.pool_name if specified")
		}

		if strings.Contains(d.config["zfs.pool_name"], "/") {
			// Handle a dataset.
			exists, err := d.datasetExists(d.config["zfs.pool_name"])
			if err != nil {
				return err
			}

			if !exists {
				err := d.createDataset(d.config["zfs.pool_name"], "mountpoint=legacy")
				if err != nil {
					return err
				}
			}
		} else {
			// Ensure that the pool is available.
			_, err := d.importPool()
			if err != nil {
				return err
			}
		}

		// Confirm that the existing pool/dataset is all empty.
		datasets, err := d.getDatasets(d.config["zfs.pool_name"], "all")
		if err != nil {
			return err
		}

		if len(datasets) > 0 {
			return fmt.Errorf(`Provided ZFS pool (or dataset) isn't empty, run "sudo zfs list -r %s" to see existing entries`, d.config["zfs.pool_name"])
		}
	}

	revert.Add(func() { _ = d.Delete(nil) })

	// Apply our default configuration.
	err = d.ensureInitialDatasets(false)
	if err != nil {
		return err
	}

	revert.Success()
	return nil
}

// Delete removes the storage pool from the storage device.
func (d *zfs) Delete(op *operations.Operation) error {
	// Check if the dataset/pool is already gone.
	exists, err := d.datasetExists(d.config["zfs.pool_name"])
	if err != nil {
		return err
	}

	if !exists {
		return nil
	}

	// Confirm that nothing's been left behind
	datasets, err := d.getDatasets(d.config["zfs.pool_name"], "all")
	if err != nil {
		return err
	}

	initialDatasets := d.initialDatasets()
	for _, dataset := range datasets {
		dataset = strings.TrimPrefix(dataset, "/")

		if slices.Contains(initialDatasets, dataset) {
			continue
		}

		fields := strings.Split(dataset, "/")
		if len(fields) > 1 {
			return fmt.Errorf("ZFS pool has leftover datasets: %s", dataset)
		}
	}

	if strings.Contains(d.config["zfs.pool_name"], "/") {
		// Delete the dataset.
		_, err := shared.RunCommandContext(context.TODO(), "zfs", "destroy", "-r", d.config["zfs.pool_name"])
		if err != nil {
			return err
		}
	} else {
		// Delete the pool.
		_, err := shared.RunCommandContext(context.TODO(), "zpool", "destroy", d.config["zfs.pool_name"])
		if err != nil {
			return err
		}
	}

	// On delete, wipe everything in the directory.
	err = wipeDirectory(GetPoolMountPath(d.name))
	if err != nil {
		return err
	}

	// Delete any loop file we may have used
	loopPath := loopFilePath(d.name)
	err = os.Remove(loopPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("Failed to remove '%s': %w", loopPath, err)
	}

	return nil
}

// Validate checks that all provide keys are supported and that no conflicting or missing configuration is present.
func (d *zfs) Validate(config map[string]string) error {
	rules := map[string]func(value string) error{
		"size": validate.Optional(validate.IsSize),
		// lxdmeta:generate(entities=storage-zfs; group=pool-conf; key=zfs.pool_name)
		//
		// ---
		//  type: string
		//  defaultdesc: name of the pool
		//  shortdesc: Name of the zpool
		//  scope: local
		"zfs.pool_name": validate.IsAny,
		// lxdmeta:generate(entities=storage-zfs; group=pool-conf; key=zfs.clone_copy)
		// Set this option to `true` or `false` to enable or disable using ZFS lightweight clones rather
		// than full dataset copies.
		// Set the option to `rebase` to copy based on the initial image.
		// ---
		//  type: string
		//  defaultdesc: `true`
		//  shortdesc: Whether to use ZFS lightweight clones
		//  scope: global
		"zfs.clone_copy": validate.Optional(func(value string) error {
			if value == "rebase" {
				return nil
			}

			return validate.IsBool(value)
		}),
		// lxdmeta:generate(entities=storage-zfs; group=pool-conf; key=zfs.export)
		//
		// ---
		//  type: bool
		//  defaultdesc: `true`
		//  shortdesc: Whether to export the zpool when an unmount is being performed
		//  scope: global
		"zfs.export": validate.Optional(validate.IsBool),
	}

	return d.validatePool(config, rules, d.commonVolumeRules())
}

// Update applies any driver changes required from a configuration change.
func (d *zfs) Update(changedConfig map[string]string) error {
	_, ok := changedConfig["zfs.pool_name"]
	if ok {
		return errors.New("zfs.pool_name cannot be modified")
	}

	size, ok := changedConfig["size"]
	if ok {
		// Figure out loop path
		loopPath := loopFilePath(d.name)

		if d.config["source"] != loopPath {
			return errors.New("Cannot resize non-loopback pools")
		}

		// Resize loop file
		f, err := os.OpenFile(loopPath, os.O_RDWR, 0600)
		if err != nil {
			return err
		}

		defer func() { _ = f.Close() }()

		sizeBytes, _ := units.ParseByteSizeString(size)

		err = f.Truncate(sizeBytes)
		if err != nil {
			return err
		}

		_, err = shared.RunCommandContext(d.state.ShutdownCtx, "zpool", "online", "-e", d.config["zfs.pool_name"], loopPath)
		if err != nil {
			return err
		}
	}

	return nil
}

// importPool the storage pool.
func (d *zfs) importPool() (bool, error) {
	if d.config["zfs.pool_name"] == "" {
		return false, fmt.Errorf("Cannot mount pool as %q is not specified", "zfs.pool_name")
	}

	// Check if already setup.
	exists, err := d.datasetExists(d.config["zfs.pool_name"])
	if err != nil {
		return false, err
	}

	if exists {
		return false, nil
	}

	// Check if the pool exists.
	poolName := strings.Split(d.config["zfs.pool_name"], "/")[0]
	exists, err = d.datasetExists(poolName)
	if err != nil {
		return false, err
	}

	if exists {
		return false, errors.New("ZFS zpool exists but dataset is missing")
	}

	// Import the pool.
	if filepath.IsAbs(d.config["source"]) {
		disksPath := shared.VarPath("disks")
		_, err := shared.RunCommandContext(d.state.ShutdownCtx, "zpool", "import", "-f", "-d", disksPath, poolName)
		if err != nil {
			return false, err
		}
	} else {
		_, err := shared.RunCommandContext(d.state.ShutdownCtx, "zpool", "import", poolName)
		if err != nil {
			return false, err
		}
	}

	// Check that the dataset now exists.
	exists, err = d.datasetExists(d.config["zfs.pool_name"])
	if err != nil {
		return false, err
	}

	if exists {
		return true, nil
	}

	return false, errors.New("ZFS zpool exists but dataset is missing")
}

// Mount mounts the storage pool.
func (d *zfs) Mount() (bool, error) {
	// Import the pool if not already imported.
	imported, err := d.importPool()
	if err != nil {
		return false, err
	}

	// Apply our default configuration.
	err = d.ensureInitialDatasets(true)
	if err != nil {
		return false, err
	}

	return imported, nil
}

// Unmount unmounts the storage pool.
func (d *zfs) Unmount() (bool, error) {
	// Skip if zfs.export config is set to false
	if shared.IsFalse(d.config["zfs.export"]) {
		return false, nil
	}

	// Skip if using a dataset and not a full pool.
	if strings.Contains(d.config["zfs.pool_name"], "/") {
		return false, nil
	}

	// Check if already unmounted.
	exists, err := d.datasetExists(d.config["zfs.pool_name"])
	if err != nil {
		return false, err
	}

	if !exists {
		return false, nil
	}

	// Export the pool.
	poolName := strings.Split(d.config["zfs.pool_name"], "/")[0]
	_, err = shared.RunCommandContext(context.TODO(), "zpool", "export", poolName)
	if err != nil {
		return false, err
	}

	return true, nil
}

// GetResources returns utilization statistics for the storage pool.
func (d *zfs) GetResources() (*api.ResourcesStoragePool, error) {
	// Get the total amount of space.
	availableStr, err := d.getDatasetProperty(d.config["zfs.pool_name"], "available")
	if err != nil {
		return nil, err
	}

	available, err := strconv.ParseUint(strings.TrimSpace(availableStr), 10, 64)
	if err != nil {
		return nil, err
	}

	// Get the used amount of space.
	usedStr, err := d.getDatasetProperty(d.config["zfs.pool_name"], "used")
	if err != nil {
		return nil, err
	}

	used, err := strconv.ParseUint(strings.TrimSpace(usedStr), 10, 64)
	if err != nil {
		return nil, err
	}

	// Build the struct.
	// Inode allocation is dynamic so no use in reporting them.
	res := api.ResourcesStoragePool{}
	res.Space.Total = used + available
	res.Space.Used = used

	return &res, nil
}

// MigrationTypes returns the type of transfer methods to be used when doing
// migrations between pools in preference order.
func (d *zfs) MigrationTypes(contentType ContentType, refresh bool, copySnapshots bool) []migration.Type {
	var rsyncFeatures []string

	// Do not pass compression argument to rsync if the associated
	// config key, that is rsync.compression, is set to false.
	if shared.IsFalse(d.Config()["rsync.compression"]) {
		rsyncFeatures = []string{"xattrs", "delete", "bidirectional"}
	} else {
		rsyncFeatures = []string{"xattrs", "delete", "compress", "bidirectional"}
	}

	// Detect ZFS features.
	features := []string{migration.ZFSFeatureMigrationHeader, "compress"}

	if contentType == ContentTypeFS {
		features = append(features, migration.ZFSFeatureZvolFilesystems)
	}

	if IsContentBlock(contentType) {
		return []migration.Type{
			{
				FSType:   migration.MigrationFSType_ZFS,
				Features: features,
			},
			{
				FSType:   migration.MigrationFSType_BLOCK_AND_RSYNC,
				Features: rsyncFeatures,
			},
		}
	}

	if refresh && !copySnapshots {
		return []migration.Type{
			{
				FSType:   migration.MigrationFSType_RSYNC,
				Features: rsyncFeatures,
			},
		}
	}

	return []migration.Type{
		{
			FSType:   migration.MigrationFSType_ZFS,
			Features: features,
		},
		{
			FSType:   migration.MigrationFSType_RSYNC,
			Features: rsyncFeatures,
		},
	}
}

// patchDropBlockVolumeFilesystemExtension removes the filesystem extension (e.g _ext4) from VM image block volumes.
func (d *zfs) patchDropBlockVolumeFilesystemExtension() error {
	poolName, ok := d.config["zfs.pool_name"]
	if !ok {
		poolName = d.name
	}

	out, err := shared.RunCommandContext(d.state.ShutdownCtx, "zfs", "list", "-H", "-r", "-o", "name", "-t", "volume", poolName+"/images")
	if err != nil {
		return fmt.Errorf("Failed listing images: %w", err)
	}

	for volume := range strings.SplitSeq(out, "\n") {
		fields := strings.SplitN(volume, poolName+"/images/", 2)

		if len(fields) != 2 || fields[1] == "" {
			continue
		}

		// Ignore non-block images, and images without filesystem extension
		if !strings.HasSuffix(fields[1], ".block") || !strings.Contains(fields[1], "_") {
			continue
		}

		// Rename zfs dataset. Snapshots will automatically be renamed.
		newName := poolName + "/images/" + strings.Split(fields[1], "_")[0] + ".block"

		_, err = shared.RunCommandContext(context.TODO(), "zfs", "rename", volume, newName)
		if err != nil {
			return fmt.Errorf("Failed renaming zfs dataset: %w", err)
		}
	}

	return nil
}

// roundVolumeBlockSizeBytes returns sizeBytes rounded up to the next multiple
// of `vol`'s "zfs.blocksize".
func (d *zfs) roundVolumeBlockSizeBytes(vol Volume, sizeBytes int64) int64 {
	minBlockSize, err := units.ParseByteSizeString(vol.ExpandedConfig("zfs.blocksize"))

	// minBlockSize will be 0 if zfs.blocksize=""
	if minBlockSize <= 0 || err != nil {
		// 16KiB is the default volblocksize
		minBlockSize = 16 * 1024
	}

	return roundAbove(minBlockSize, sizeBytes)
}
