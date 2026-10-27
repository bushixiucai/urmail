// Package archiveevidence assembles verifiable evidence packages for approved archive investigations.
package archiveevidence

import (
	"github.com/bushixiucai/urmail/internal/domain/evidence"
)

// Original is one approved archive source.
type Original struct {
	ID    string
	Bytes []byte
}

// Package builds a manifest from exact original bytes.
// It does not read the filesystem or authorize the caller.
func Package(approvalDigest string, originals []Original) (evidence.Manifest, error) {
	sources := make([]evidence.Source, 0, len(originals))
	for _, original := range originals {
		sources = append(sources, evidence.SourceFromOriginal(original.ID, original.Bytes))
	}
	return evidence.Build(approvalDigest, sources)
}
