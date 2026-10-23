package nsd

import (
	"fmt"
	"strings"
)

// FileZonesConf is the relative path of the zones config file in nsd conf.d.
const FileZonesConf = "zones.conf"

// Generate is a pure function that generates nsd configuration and zone files.
// Returned map keys:
//
//	"zones.conf" -> /etc/nsd/nsd.conf.d/zones.conf
//	"zones/<domain>.txt" -> /etc/nsd/zones/<domain>.txt
func Generate(node NodeConfig, snap Snapshot) (map[string]string, error) {
	if err := Validate(node, snap); err != nil {
		return nil, err
	}

	result := make(map[string]string, len(snap.Zones)+1)
	result[FileZonesConf] = renderZonesConf(node, snap)

	for _, z := range snap.Zones {
		domain := strings.ToLower(strings.TrimSuffix(z.Domain, "."))
		key := fmt.Sprintf("zones/%s.txt", domain)
		result[key] = renderZoneFile(node, z)
	}

	return result, nil
}
