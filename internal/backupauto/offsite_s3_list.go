package backupauto

import (
	"encoding/xml"
	"fmt"
	"strings"
	"time"
)

// s3ListXML is the subset of ListBucketV2 output the planner needs.
type s3ListXML struct {
	XMLName          xml.Name
	Name             string           `xml:"Name"`
	Prefix           string           `xml:"Prefix"`
	IsTruncated      *bool            `xml:"IsTruncated"`
	NextContinuation string           `xml:"NextContinuationToken"`
	Contents         []s3ListContents `xml:"Contents"`
}

type s3ListContents struct {
	Key          string    `xml:"Key"`
	Size         int64     `xml:"Size"`
	LastModified time.Time `xml:"LastModified"`
}

// parseS3ListXML accepts only a complete, bounded single-page inventory.
// Truncation is refused instead of using partial evidence to delete backups.
func parseS3ListXML(body, prefix string) ([]RemoteObject, error) {
	var parsed s3ListXML
	if err := xml.Unmarshal([]byte(body), &parsed); err != nil {
		return nil, fmt.Errorf("decode s3 listing: %w", err)
	}
	if parsed.Name == "" {
		return nil, fmt.Errorf("decode s3 listing: response lacks bucket name")
	}
	if parsed.IsTruncated == nil || *parsed.IsTruncated || parsed.NextContinuation != "" {
		return nil, fmt.Errorf("s3 listing is truncated; refusing incomplete retention inventory")
	}
	if len(parsed.Contents) > retentionMaxObjects {
		return nil, fmt.Errorf("s3 listing object budget exceeded")
	}
	objects := make([]RemoteObject, 0, len(parsed.Contents))
	for _, c := range parsed.Contents {
		if prefix != "" && !strings.HasPrefix(c.Key, strings.Trim(prefix, "/")+"/") {
			return nil, fmt.Errorf("s3 listing escaped configured prefix")
		}

		name := strings.TrimPrefix(c.Key, prefix)
		name = strings.TrimPrefix(name, "/")
		if name == "" {
			continue
		}
		objects = append(objects, RemoteObject{Name: name, Size: c.Size, LastModified: c.LastModified})
	}
	return objects, nil
}
