// SPDX-License-Identifier: AGPL-3.0-only

package ramp

import (
	"strings"
	"testing"
)

func TestBuildResourceName_Stable(t *testing.T) {
	got1 := BuildResourceName("Acme Corp", "ramp-123")
	got2 := BuildResourceName("Acme Corp", "ramp-123")
	if got1 != got2 {
		t.Fatalf("expected stable name for same input, got %q vs %q", got1, got2)
	}
	if !strings.HasPrefix(got1, "acme-corp-") {
		t.Fatalf("expected slug prefix, got %q", got1)
	}
}

func TestBuildResourceName_RespectsDNSLabelLimit(t *testing.T) {
	long := strings.Repeat("Acme Corporation ", 10) // ~170 chars
	name := BuildResourceName(long, "id")
	if len(name) > 63 {
		t.Fatalf("name %q exceeds 63-char DNS label limit", name)
	}
}

func TestBuildResourceName_FallbackForEmptySlug(t *testing.T) {
	name := BuildResourceName("!!!", "abc")
	if !strings.HasPrefix(name, "ramp-vendor-") {
		t.Fatalf("expected ramp-vendor fallback, got %q", name)
	}
}

func TestBuildResourceName_DifferentIDsProduceDifferentSuffixes(t *testing.T) {
	a := BuildResourceName("Acme Corp", "id-a")
	b := BuildResourceName("Acme Corp", "id-b")
	if a == b {
		t.Fatalf("expected different suffixes for different vendor IDs, both %q", a)
	}
}

func TestMapVendor_PopulatesDisplayAndLegalEntity(t *testing.T) {
	mapped, ok := MapVendor(AccountingVendor{
		ID:       "ramp-123",
		Name:     "Stripe Payments Europe Ltd",
		IsActive: true,
	})
	if !ok {
		t.Fatal("expected MapVendor to return ok=true for a valid Ramp vendor")
	}
	if mapped.RampVendorID != "ramp-123" {
		t.Fatalf("expected RampVendorID=ramp-123, got %q", mapped.RampVendorID)
	}
	if mapped.DisplayName != "Stripe Payments Europe Ltd" {
		t.Fatalf("unexpected DisplayName: %q", mapped.DisplayName)
	}
	if mapped.LegalEntity != "Stripe Payments Europe Ltd" {
		t.Fatalf("unexpected LegalEntity: %q", mapped.LegalEntity)
	}
	if mapped.CountryOfIncorporation != "UN" {
		t.Fatalf(
			"expected CountryOfIncorporation default UN, got %q",
			mapped.CountryOfIncorporation,
		)
	}
}

func TestMapVendor_RejectsBlankInputs(t *testing.T) {
	cases := []AccountingVendor{
		{ID: "", Name: "Some Vendor"},
		{ID: "ramp-1", Name: ""},
		{ID: "ramp-1", Name: "   "},
	}
	for i, in := range cases {
		if _, ok := MapVendor(in); ok {
			t.Fatalf("case %d: expected MapVendor to return ok=false for %+v", i, in)
		}
	}
}
