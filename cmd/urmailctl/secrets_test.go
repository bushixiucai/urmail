package main

import "testing"

func TestLegacyTOTPValidationDoesNotAcceptCiphertextOrUnboundedData(t *testing.T) {
	for _, seed := range []string{"JBSWY3DPEHPK3PXP", "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP"} {
		if !validLegacyTOTP(seed) {
			t.Fatal("valid legacy seed rejected")
		}
	}
	for _, seed := range []string{"", "short", "enc:v1:invalid", "JBSWY3DPEHPK3PXP ", "JBSWY3DPEHPK3PX!"} {
		if validLegacyTOTP(seed) {
			t.Fatal("invalid seed accepted")
		}
	}
}
