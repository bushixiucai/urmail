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
	IsTruncated      bool             `xml:"IsTruncated"`
	NextContinuation string           `xml:"NextContinuationToken"`
	Contents         []s3ListContents `xml:"Contents"`
}

type s3ListContents struct {
	Key          string    `xml:"Key"`
	Size         int64     `xml:"Size"`
	LastModified time.Time `xml:"LastModified"`
}

// parseS3ListXML decodes one listing page and strips the destination prefix.
// Continuation pages are handled by the caller re-issuing the request with
// continuation-token; the local stores here keep the flat prefix listing
// because retention generations stay well below one page (1000 keys) for a
// 7/4/12 policy. The parser is lenient about prefixes so plain-bucket
// destinations (no prefix) also work.
func parseS3ListXML(body, prefix string) ([]RemoteObject, error) {
	var parsed s3ListXML
	if err := xml.Unmarshal([]byte(body), &parsed); err != nil {
		return nil, fmt.Errorf("decode s3 listing: %w", err)
	}
	if parsed.Name == "" {
		return nil, fmt.Errorf("decode s3 listing: response lacks bucket name")
	}
	objects := make([]RemoteObject, 0, len(parsed.Contents))
	for _, c := range parsed.Contents {
		name := strings.TrimPrefix(c.Key, prefix)
		name = strings.TrimPrefix(name, "/")
		if name == "" {
			continue
		}
		objects = append(objects, RemoteObject{Name: name, Size: c.Size, LastModified: c.LastModified})
	}
	return objects, nil
}
