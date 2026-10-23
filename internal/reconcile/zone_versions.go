package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/bushixiucai/urmail/internal/mail/nsd"
	"github.com/bushixiucai/urmail/internal/store"
)

// ZoneVersionStore reserves atomic per-zone content versions.
type ZoneVersionStore interface {
	ReserveZoneSerial(context.Context, string, string, uint32) (uint32, error)
}

// VersionedStore adds persistent zone versioning without moving I/O into generators.
type VersionedStore struct {
	StoreQuerier
	DB store.DBTX
}

// ReserveZoneSerial advances only when normalized zone content changes.
func (s *VersionedStore) ReserveZoneSerial(ctx context.Context, domain, hash string, seed uint32) (uint32, error) {
	serial, err := store.New(s.DB).ReserveDNSZoneVersion(ctx, store.ReserveDNSZoneVersionParams{Domain: domain, ContentHash: hash, Serial: (int64(seed) + 1) % 4294967296})
	if err != nil {
		return 0, err
	}
	if serial < 0 || serial > 4294967295 {
		return 0, fmt.Errorf("invalid DNS serial")
	}
	return uint32(serial), nil // #nosec G115 -- range checked above.
}

func reserveZoneSerials(ctx context.Context, q StoreQuerier, node nsd.NodeConfig, snapshot *nsd.Snapshot) error {
	versions, ok := q.(ZoneVersionStore)
	if !ok {
		return nil
	}
	return ReserveDNSVersions(ctx, versions, node, snapshot)
}

// ReserveDNSVersions reserves durable content versions before writing zone files.
func ReserveDNSVersions(ctx context.Context, versions ZoneVersionStore, node nsd.NodeConfig, snapshot *nsd.Snapshot) error {
	for i := range snapshot.Zones {
		zone := snapshot.Zones[i]
		seed := zone.Serial
		zone.Serial = 0
		rendered, err := nsd.Generate(node, nsd.Snapshot{Zones: []nsd.Zone{zone}})
		if err != nil {
			return err
		}
		sum := sha256.Sum256([]byte(rendered["zones/"+zone.Domain+".txt"]))
		hash := hex.EncodeToString(sum[:])
		serial, err := versions.ReserveZoneSerial(ctx, zone.Domain, hash, seed)
		if err != nil {
			return fmt.Errorf("reserve DNS serial: %w", err)
		}
		snapshot.Zones[i].Serial = serial
	}
	return nil
}
