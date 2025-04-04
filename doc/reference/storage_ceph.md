---
discourse: lxc:[Introducing&#32;MicroCeph](15457)
---

(storage-ceph)=
# Ceph RBD - `ceph`

```{youtube} https://youtube.com/watch?v=kVLGbvRU98A
```

<!-- Include start Ceph intro -->
[Ceph](https://ceph.io/en/) is an open-source storage platform that stores its data in a storage cluster based on {abbr}`RADOS (Reliable Autonomic Distributed Object Store)`.
It is highly scalable and, as a distributed system without a single point of failure, very reliable.

```{tip}
If you want to quickly set up a basic Ceph cluster, check out [MicroCeph](https://canonical.com/microcloud).
```

Ceph provides different components for block storage and for file systems.
<!-- Include end Ceph intro -->

Ceph {abbr}`RBD (RADOS Block Device)` is Ceph's block storage component that distributes data and workload across the Ceph cluster.
It uses thin provisioning, which means that it is possible to over-commit resources.

## Terminology

<!-- Include start Ceph terminology -->
Ceph uses the term *object* for the data that it stores.
The daemon that is responsible for storing and managing data is the *Ceph {abbr}`OSD (Object Storage Daemon)`*.
Ceph's storage is divided into *pools*, which are logical partitions for storing objects.
They are also referred to as *data pools*, *storage pools* or *OSD pools*.
<!-- Include end Ceph terminology -->

Ceph block devices are also called *RBD images*, and you can create *snapshots* and *clones* of these RBD images.

## `ceph` driver in LXD

```{note}
To use the Ceph RBD driver, you must specify it as `ceph`.
This is slightly misleading, because it uses only Ceph RBD (block storage) functionality, not full Ceph functionality.
For storage volumes with content type `filesystem` (images, containers and custom file-system volumes), the `ceph` driver uses Ceph RBD images with a file system on top (see {config:option}`storage-ceph-volume-conf:block.filesystem`).

Alternatively, you can use the {ref}`CephFS <storage-cephfs>` driver to create storage volumes with content type `filesystem`.
```

<!-- Include start Ceph driver cluster -->
Unlike other storage drivers, this driver does not set up the storage system but assumes that you already have a Ceph cluster installed.
<!-- Include end Ceph driver cluster -->

<!-- Include start Ceph driver remote -->
This driver also behaves differently than other drivers in that it provides remote storage.
As a result and depending on the internal network, storage access might be a bit slower than for local storage.
On the other hand, using remote storage has big advantages in a cluster setup, because all cluster members have access to the same storage pools with the exact same contents, without the need to synchronize storage pools.
<!-- Include end Ceph driver remote -->

The `ceph` driver in LXD uses RBD images for images, and snapshots and clones to create instances and snapshots.

<!-- Include start Ceph driver control -->
LXD assumes that it has full control over the OSD storage pool.
Therefore, you should never maintain any file system entities that are not owned by LXD in a LXD OSD storage pool, because LXD might delete them.
<!-- Include end Ceph driver control -->

Due to the way copy-on-write works in Ceph RBD, parent RBD images can't be removed until all children are gone.
As a result, LXD automatically renames any objects that are removed but still referenced.
Such objects are kept with a  `zombie_` prefix until all references are gone and the object can safely be removed.

### Limitations

The `ceph` driver has the following limitations:

Sharing custom volumes between instances
: Custom storage volumes with {ref}`content type <storage-content-types>` `filesystem` can usually be shared between multiple instances different cluster members.
  However, because the Ceph RBD driver "simulates" volumes with content type `filesystem` by putting a file system on top of an RBD image, custom storage volumes can only be assigned to a single instance at a time.
  If you need to share a custom volume with content type `filesystem`, use the {ref}`CephFS <storage-cephfs>` driver instead.

Sharing the OSD storage pool between installations
: Sharing the same OSD storage pool between multiple LXD installations is not supported.

Using an OSD pool of type "erasure"
: To use a Ceph OSD pool of type "erasure", you must create the OSD pool beforehand.
  You must also create a separate OSD pool of type "replicated" that will be used for storing metadata.
  This is required because Ceph RBD does not support `omap`.
  To specify which pool is "erasure coded", set the {config:option}`storage-ceph-pool-conf:ceph.osd.data_pool_name` configuration option to the erasure coded pool name and the {config:option}`storage-ceph-pool-conf:source` configuration option to the replicated pool name.

## Configuration options

The following configuration options are available for storage pools that use the `ceph` driver and for storage volumes in these pools.

(storage-ceph-pool-config)=
### Storage pool configuration

% Include content from [../metadata.txt](../metadata.txt)
```{include} ../metadata.txt
    :start-after: <!-- config group storage-ceph-pool-conf start -->
    :end-before: <!-- config group storage-ceph-pool-conf end -->
```

{{volume_configuration}}

(storage-ceph-vol-config)=
### Storage volume configuration

% Include content from [../metadata.txt](../metadata.txt)
```{include} ../metadata.txt
    :start-after: <!-- config group storage-ceph-volume-conf start -->
    :end-before: <!-- config group storage-ceph-volume-conf end -->
```
