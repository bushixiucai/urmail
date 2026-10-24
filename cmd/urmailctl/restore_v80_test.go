package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func mutateV80Archive(t *testing.T, manifestPath string, edit func(*tar.Header, []byte) (*tar.Header, []byte)) string {
	t.Helper()
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest v80Manifest
	if err = json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(filepath.Dir(manifestPath), manifest.Artifact.Name)
	old, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(old))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gz.Close() }()
	reader := tar.NewReader(gz)
	var buffer bytes.Buffer
	writerGzip := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(writerGzip)
	for {
		header, readErr := reader.Next()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			t.Fatal(readErr)
		}
		body, bodyErr := io.ReadAll(reader)
		if bodyErr != nil {
			t.Fatal(bodyErr)
		}
		header, body = edit(header, body)
		if err = writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err = writer.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err = writerGzip.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(archive, buffer.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest.Artifact.Size = int64(buffer.Len())
	manifest.Artifact.SHA256, err = fileSHA256(archive)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(manifestPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return archive
}

func TestV80ProfileRestoreRejectsRehashedTarAttacks(t *testing.T) {
	for _, scenario := range []string{"path-traversal", "symlink-escape", "owner-metadata", "changed-content"} {
		t.Run(scenario, func(t *testing.T) {
			base := v80Fixture(t)
			inventory, err := collectV80Inventory(base)
			if err != nil {
				t.Fatal(err)
			}
			manifest, err := createV80Bundle(context.Background(), base, t.TempDir(), inventory, false)
			if err != nil {
				t.Fatal(err)
			}
			changed := false
			archive := mutateV80Archive(t, manifest, func(header *tar.Header, body []byte) (*tar.Header, []byte) {
				if changed {
					return header, body
				}
				switch scenario {
				case "path-traversal":
					header.Name = "../../etc/passwd"
					changed = true
				case "symlink-escape":
					if header.Typeflag == tar.TypeSymlink {
						header.Linkname = "/etc/passwd"
						changed = true
					}
				case "owner-metadata":
					header.Uid++
					changed = true
				case "changed-content":
					if len(body) > 0 {
						body[0] ^= 1
						changed = true
					}
				}
				return header, body
			})
			if !changed {
				t.Fatal("attack fixture did not change a member")
			}
			if _, err = verifyV80Bundle(context.Background(), manifest, archive); err == nil {
				t.Fatal("rehashed malformed archive passed profile preview")
			}
		})
	}
}

func TestV80ProfileRestoreRejectsTamperedWholeArtifact(t *testing.T) {
	base := v80Fixture(t)
	inventory, err := collectV80Inventory(base)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath, err := createV80Bundle(context.Background(), base, t.TempDir(), inventory, false)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest v80Manifest
	if err = json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(filepath.Dir(manifestPath), manifest.Artifact.Name)
	original, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	original[len(original)-1] ^= 1
	if err = os.WriteFile(archive, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = verifyV80Bundle(context.Background(), manifestPath, archive); err == nil {
		t.Fatal("whole artifact corruption was accepted")
	}
}
