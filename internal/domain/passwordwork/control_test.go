package passwordwork

import "testing"

// Newly chosen passwords must not contain control characters; they corrupt
// logs and terminal entry flows (same rule as mailbox password changes).
func TestValidateNewRejectsControlCharacters(t *testing.T) {
	for _, p := range []string{
		"password-with\x07-bell!",
		"password\ttabbed-long",
		"password\nnewline-long",
		"password\x1b-escape-long",
		"password\x00-nul-long!",
	} {
		if ValidateNew(p) == nil {
			t.Fatalf("control-character password accepted: %q", p)
		}
	}
	// Printable UTF-8 of the same shape stays valid.
	for _, p := range []string{
		"password-with-bell!",
		"password——em-dash-long",
	} {
		if err := ValidateNew(p); err != nil {
			t.Fatalf("printable password rejected: %q: %v", p, err)
		}
	}
}
