package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/canonical/lxd/client"
	"github.com/canonical/lxd/shared/api"
)

type cmdMigratedumpsuccess struct {
	global *cmdGlobal
}

// Command returns a cobra.Command object representing the "migratedumpsuccess" command.
func (c *cmdMigratedumpsuccess) Command() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Use = "migratedumpsuccess <operation> <secret>"
	cmd.Short = "Tell LXD that a particular CRIU dump succeeded"
	cmd.Long = `Description:
  Tell LXD that a particular CRIU dump succeeded

  This internal command is used from the CRIU dump script and is
  called as soon as the script is done running.
`
	cmd.RunE = c.Run
	cmd.Hidden = true

	return cmd
}

// Run executes the "migratedumpsuccess" command.
func (c *cmdMigratedumpsuccess) Run(cmd *cobra.Command, args []string) error {
	// Quick checks.
	if len(args) < 2 {
		_ = cmd.Help()

		if len(args) == 0 {
			return nil
		}

		return errors.New("Missing required arguments")
	}

	// Only root should run this
	if os.Geteuid() != 0 {
		return errors.New("This must be run as root")
	}

	lxdArgs := lxd.ConnectionArgs{
		SkipGetServer: true,
	}

	d, err := lxd.ConnectLXDUnix("", &lxdArgs)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/websocket?secret=%s", strings.TrimPrefix(args[0], "/1.0"), args[1])
	conn, err := d.RawWebsocket(url)
	if err != nil {
		return err
	}

	_ = conn.Close()

	resp, _, err := d.RawQuery(http.MethodGet, args[0]+"/wait", nil, "")
	if err != nil {
		return err
	}

	op, err := resp.MetadataAsOperation()
	if err != nil {
		return err
	}

	if op.StatusCode == api.Success {
		return nil
	}

	return errors.New(op.Err)
}
