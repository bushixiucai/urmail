package reconcile

import (
	"context"
	"fmt"

	"github.com/bushixiucai/urmail/internal/mail/nsd"
)

func attachDNSSecurity(ctx context.Context, q StoreQuerier, zones []nsd.Zone) error {
	rows, err := q.PlatformListDNSSecurity(ctx)
	if err != nil {
		return fmt.Errorf("read authoritative DNS security intent: %w", err)
	}
	byName := map[string]int{}
	for index, zone := range zones {
		byName[zone.Domain] = index
	}
	for _, row := range rows {
		index, ok := byName[row.Fqdn]
		if !ok {
			continue
		}
		zones[index].Sign = row.DnssecEnabled
		if row.SecondaryHostname != "" {
			zones[index].Secondary = &nsd.SecondaryConfig{Hostname: row.SecondaryHostname, Address: row.SecondaryAddress, PrimaryAddress: row.PrimaryAddress, TSIGKeyName: row.TsigKeyName}
		}
	}
	return nil
}
