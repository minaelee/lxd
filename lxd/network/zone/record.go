package zone

import (
	"context"
	"fmt"
	"slices"

	"github.com/miekg/dns"

	"github.com/canonical/lxd/lxd/cluster/request"
	"github.com/canonical/lxd/lxd/db"
	"github.com/canonical/lxd/shared/api"
)

// AddRecord adds a network zone record.
func (d *zone) AddRecord(req api.NetworkZoneRecordsPost) error {
	// lxdmeta:generate(entities=network-zone; group=record-properties; key=name)
	//
	// ---
	//  type: string
	//  required: yes
	//  shortdesc: Unique name of the record

	// lxdmeta:generate(entities=network-zone; group=record-properties; key=description)
	//
	// ---
	//  type: string
	//  required: no
	//  shortdesc: Description of the record

	// lxdmeta:generate(entities=network-zone; group=record-properties; key=entries)
	//
	// ---
	//  type: entry list
	//  required: no
	//  shortdesc: List of DNS entries

	// lxdmeta:generate(entities=network-zone; group=record-properties; key=config)
	// The only supported keys are `user.*` custom keys.
	// ---
	//  type: string set
	//  required: no
	//  shortdesc: User-provided free-form key/value pairs

	// Validate.
	err := d.validateRecordConfig(req.NetworkZoneRecordPut)
	if err != nil {
		return err
	}

	// Validate entries.
	err = d.validateEntries(req.NetworkZoneRecordPut)
	if err != nil {
		return err
	}

	err = d.state.DB.Cluster.Transaction(context.TODO(), func(ctx context.Context, tx *db.ClusterTx) error {
		// Add the new record.
		_, err = tx.CreateNetworkZoneRecord(ctx, d.id, req)

		return err
	})
	if err != nil {
		return err
	}

	return nil
}

// GetRecords fetches the network zone records.
func (d *zone) GetRecords() ([]api.NetworkZoneRecord, error) {
	s := d.state

	var names []string
	records := []api.NetworkZoneRecord{}
	var record *api.NetworkZoneRecord

	err := s.DB.Cluster.Transaction(context.TODO(), func(ctx context.Context, tx *db.ClusterTx) error {
		var err error

		// Get the record names.
		names, err = tx.GetNetworkZoneRecordNames(ctx, d.id)
		if err != nil {
			return err
		}

		// Load all the records.
		for _, name := range names {
			_, record, err = tx.GetNetworkZoneRecord(ctx, d.id, name)
			if err != nil {
				return err
			}

			records = append(records, *record)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return records, nil
}

// GetRecord fetches the network zone record corresponding to the given name.
func (d *zone) GetRecord(name string) (*api.NetworkZoneRecord, error) {
	var record *api.NetworkZoneRecord

	err := d.state.DB.Cluster.Transaction(context.TODO(), func(ctx context.Context, tx *db.ClusterTx) error {
		var err error

		// Get the record.
		_, record, err = tx.GetNetworkZoneRecord(ctx, d.id, name)

		return err
	})
	if err != nil {
		return nil, err
	}

	return record, nil
}

// UpdateRecord updates the network zone record corresponding to the given name.
func (d *zone) UpdateRecord(name string, req api.NetworkZoneRecordPut, clientType request.ClientType) error {
	s := d.state

	// Validate.
	err := d.validateRecordConfig(req)
	if err != nil {
		return err
	}

	// Validate entries.
	err = d.validateEntries(req)
	if err != nil {
		return err
	}

	err = s.DB.Cluster.Transaction(context.TODO(), func(ctx context.Context, tx *db.ClusterTx) error {
		// Get the record.
		id, _, err := tx.GetNetworkZoneRecord(ctx, d.id, name)
		if err != nil {
			return err
		}

		// Update the record.
		err = tx.UpdateNetworkZoneRecord(ctx, id, req)
		if err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		return err
	}

	return nil
}

// DeleteRecord delete the network zone record corresponding to the given name.
func (d *zone) DeleteRecord(name string) error {
	s := d.state

	err := s.DB.Cluster.Transaction(context.TODO(), func(ctx context.Context, tx *db.ClusterTx) error {
		// Get the record.
		id, _, err := tx.GetNetworkZoneRecord(ctx, d.id, name)
		if err != nil {
			return err
		}

		// Delete the record.
		err = tx.DeleteNetworkZoneRecord(ctx, id)
		if err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		return err
	}

	return nil
}

// validateRecordConfig checks the config and rules are valid.
func (d *zone) validateRecordConfig(info api.NetworkZoneRecordPut) error {
	rules := map[string]func(value string) error{}

	err := d.validateConfigMap(info.Config, rules)
	if err != nil {
		return err
	}

	return nil
}

// validateEntries checks the validity of the DNS entries.
func (d *zone) validateEntries(info api.NetworkZoneRecordPut) error {
	uniqueEntries := make([]string, 0, len(info.Entries))

	for _, entry := range info.Entries {
		if entry.TTL == 0 {
			entry.TTL = 300
		}

		_, err := dns.NewRR(fmt.Sprintf("record %d IN %s %s", entry.TTL, entry.Type, entry.Value))
		if err != nil {
			return fmt.Errorf("Bad zone record entry: %w", err)
		}

		entryID := entry.Type + "/" + entry.Value
		if slices.Contains(uniqueEntries, entryID) {
			return fmt.Errorf("Duplicate record for type %q and value %q", entry.Type, entry.Value)
		}

		uniqueEntries = append(uniqueEntries, entryID)
	}

	return nil
}
