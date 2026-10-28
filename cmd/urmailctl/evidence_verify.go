package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/bushixiucai/urmail/internal/archiveevidence"
)

func runEvidence(args []string) error {
	if len(args) == 0 || args[0] != "verify" {
		return fmt.Errorf("evidence requires verify --manifest-sha256 SHA256 FILE.zip")
	}
	flags := flag.NewFlagSet("evidence verify", flag.ContinueOnError)
	digest := flags.String("manifest-sha256", "", "trusted expected manifest SHA256")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 1 || *digest == "" {
		return fmt.Errorf("verification requires --manifest-sha256 and one package file")
	}
	file, err := os.Open(flags.Arg(0))
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	stat, err := file.Stat()
	if err != nil {
		return err
	}
	doc, err := archiveevidence.VerifyPackage(file, stat.Size(), *digest)
	if err != nil {
		return err
	}
	fmt.Printf("Verified case=%d approval=%d originals=%d manifest_sha256=%s\n", doc.CaseID, doc.ApprovalID, len(doc.Files), *digest)
	return nil
}
