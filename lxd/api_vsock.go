package main

import (
	"context"
	"crypto/x509"
	"fmt"
	"net/http"

	"github.com/canonical/lxd/lxd/db"
	"github.com/canonical/lxd/lxd/db/cluster"
	"github.com/canonical/lxd/lxd/instance"
	"github.com/canonical/lxd/lxd/state"
	"github.com/canonical/lxd/lxd/util"
)

func authenticateAgentCert(s *state.State, r *http.Request) (bool, instance.Instance, error) {
	var vsockID int
	trusted := false

	_, err := fmt.Sscanf(r.RemoteAddr, "vm(%d)", &vsockID)
	if err != nil {
		return false, nil, err
	}

	var clusterInst *cluster.Instance

	err = s.DB.Cluster.Transaction(context.TODO(), func(ctx context.Context, tx *db.ClusterTx) error {
		var err error

		clusterInst, err = tx.GetLocalInstanceWithVsockID(ctx, vsockID)
		if err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		return false, nil, err
	}

	inst, err := instance.LoadByProjectAndName(s, clusterInst.Project, clusterInst.Name)
	if err != nil {
		return false, nil, err
	}

	agentCert := inst.(instance.VM).AgentCertificate()
	trustedCerts := map[string]x509.Certificate{"0": *agentCert}

	for _, cert := range r.TLS.PeerCertificates {
		trusted, _ = util.CheckMutualTLS(*cert, trustedCerts)
		if trusted {
			return true, inst, nil
		}
	}

	return false, nil, nil
}
