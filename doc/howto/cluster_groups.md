---
discourse: lxc:[Cluster&#32;server&#32;grouping](12716)
---

(howto-cluster-groups)=
# How to set up cluster groups

```{youtube} https://www.youtube.com/watch?v=t_3YJo_xItM
```

Cluster members can be assigned to {ref}`cluster-groups`.
By default, all cluster members belong to the `default` group.

To create a cluster group, use the [`lxc cluster group create`](lxc_cluster_group_create.md) command.
For example:

    lxc cluster group create gpu

To assign a cluster member to one or more groups, use the [`lxc cluster group assign`](lxc_cluster_group_assign.md) command.
This command removes the specified cluster member from all the cluster groups it currently is a member of and then adds it to the specified group or groups.

For example, to assign `server1` to only the `gpu` group, use the following command:

    lxc cluster group assign server1 gpu

To assign `server1` to the `gpu` group and also keep it in the `default` group, use the following command:

    lxc cluster group assign server1 default,gpu

To add a cluster member to a specific group without removing it from other groups, use the [`lxc cluster group add`](lxc_cluster_group_add.md) command.

For example, to add `server1` to the `gpu` group and also keep it in the `default` group, use the following command:

    lxc cluster group add server1 gpu

## Launch an instance on a cluster group member

With cluster groups, you can target an instance to run on one of the members of the cluster group, instead of targeting it to run on a specific member.

```{note}
{config:option}`cluster-cluster:scheduler.instance` must be set to either `all` (the default) or `group` to allow instances to be targeted to a cluster group.

See {ref}`clustering-instance-placement` for more information.
```

To launch an instance on a member of a cluster group, follow the instructions in {ref}`cluster-target-instance`, but use the group name prefixed with `@` for the `--target` flag.
For example:

    lxc launch ubuntu:24.04 c1 --target=@gpu
