package main

import (
	"errors"
	"fmt"
	"slices"

	"github.com/spf13/cobra"

	"github.com/canonical/lxd/shared"
	cli "github.com/canonical/lxd/shared/cmd"
	"github.com/canonical/lxd/shared/i18n"
)

type cmdClusterRole struct {
	global  *cmdGlobal
	cluster *cmdCluster
}

// It uses the cmdGlobal, cmdCluster, and cmdClusterRole structs for context and operation.
func (c *cmdClusterRole) command() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Use = usage("role")
	cmd.Short = i18n.G("Manage cluster roles")
	cmd.Long = cli.FormatSection(i18n.G("Description"), i18n.G(`Manage cluster roles`))

	// Add
	clusterRoleAddCmd := cmdClusterRoleAdd{global: c.global, cluster: c.cluster, clusterRole: c}
	cmd.AddCommand(clusterRoleAddCmd.command())

	// Remove
	clusterRoleRemoveCmd := cmdClusterRoleRemove{global: c.global, cluster: c.cluster, clusterRole: c}
	cmd.AddCommand(clusterRoleRemoveCmd.command())

	// Workaround for subcommand usage errors. See: https://github.com/spf13/cobra/issues/706
	cmd.Args = cobra.NoArgs
	cmd.Run = func(cmd *cobra.Command, args []string) { _ = cmd.Usage() }
	return cmd
}

type cmdClusterRoleAdd struct {
	global      *cmdGlobal
	cluster     *cmdCluster
	clusterRole *cmdClusterRole
}

// Setting up the usage, short description, and long description of the command, as well as its RunE method.
func (c *cmdClusterRoleAdd) command() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Use = usage("add", i18n.G("[<remote>:]<member> <role[,role...]>"))
	cmd.Short = i18n.G("Add roles to a cluster member")
	cmd.Long = cli.FormatSection(i18n.G("Description"), i18n.G(
		`Add roles to a cluster member`))

	cmd.RunE = c.run

	cmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) == 0 {
			return c.global.cmpTopLevelResource("cluster_member", toComplete)
		}

		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	return cmd
}

// It checks and parses input arguments, verifies role assignment, and updates the member's roles.
func (c *cmdClusterRoleAdd) run(cmd *cobra.Command, args []string) error {
	exit, err := c.global.CheckArgs(cmd, args, 2, 2)
	if exit {
		return err
	}

	resources, err := c.global.ParseServers(args[0])
	if err != nil {
		return err
	}

	resource := resources[0]

	if resource.name == "" {
		return errors.New(i18n.G("Missing cluster member name"))
	}

	// Extract the current value
	member, etag, err := resource.server.GetClusterMember(resource.name)
	if err != nil {
		return err
	}

	memberWritable := member.Writable()
	newRoles := shared.SplitNTrimSpace(args[1], ",", -1, false)
	for _, newRole := range newRoles {
		if slices.Contains(memberWritable.Roles, newRole) {
			return fmt.Errorf(i18n.G("Member %q already has role %q"), resource.name, newRole)
		}
	}

	memberWritable.Roles = append(memberWritable.Roles, newRoles...)

	return resource.server.UpdateClusterMember(resource.name, memberWritable, etag)
}

type cmdClusterRoleRemove struct {
	global      *cmdGlobal
	cluster     *cmdCluster
	clusterRole *cmdClusterRole
}

// Removing the roles from a cluster member, setting up usage, descriptions, and the RunE method.
func (c *cmdClusterRoleRemove) command() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Use = usage("remove", i18n.G("[<remote>:]<member> <role[,role...]>"))
	cmd.Short = i18n.G("Remove roles from a cluster member")
	cmd.Long = cli.FormatSection(i18n.G("Description"), i18n.G(
		`Remove roles from a cluster member`))

	cmd.RunE = c.run

	cmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) == 0 {
			return c.global.cmpTopLevelResource("cluster_member", toComplete)
		}

		if len(args) == 1 {
			return c.global.cmpClusterMemberRoles(args[0])
		}

		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	return cmd
}

// Run executes the removal of specified roles from a cluster member, checking inputs, validating role assignment, and updating the member's roles.
func (c *cmdClusterRoleRemove) run(cmd *cobra.Command, args []string) error {
	exit, err := c.global.CheckArgs(cmd, args, 2, 2)
	if exit {
		return err
	}

	resources, err := c.global.ParseServers(args[0])
	if err != nil {
		return err
	}

	resource := resources[0]

	if resource.name == "" {
		return errors.New(i18n.G("Missing cluster member name"))
	}

	// Extract the current value
	member, etag, err := resource.server.GetClusterMember(resource.name)
	if err != nil {
		return err
	}

	memberWritable := member.Writable()
	rolesToRemove := shared.SplitNTrimSpace(args[1], ",", -1, false)
	for _, roleToRemove := range rolesToRemove {
		if !slices.Contains(memberWritable.Roles, roleToRemove) {
			return fmt.Errorf(i18n.G("Member %q does not have role %q"), resource.name, roleToRemove)
		}
	}

	memberWritable.Roles = shared.RemoveElementsFromSlice(memberWritable.Roles, rolesToRemove...)

	return resource.server.UpdateClusterMember(resource.name, memberWritable, etag)
}
