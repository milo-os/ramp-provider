// SPDX-License-Identifier: AGPL-3.0-only

package ramp

import (
	"crypto/sha1" //nolint:gosec // SHA-1 here is used as a stable name suffix, not a security boundary.
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// MappedVendor is the provider-owned shape produced from a single Ramp
// accounting vendor. The syncer turns these into either a new Vendor CR
// or a patch over an existing Draft Vendor — without depending on the
// compliance API package directly, so the schema can evolve there
// without churn here.
type MappedVendor struct {
	// Name is a deterministic, RFC 1123-safe Kubernetes resource name
	// derived from the Ramp vendor name and ID. Used when creating new
	// Vendor records; existing CRs are looked up by the RampVendorID
	// annotation, not by name.
	Name string

	// RampVendorID is the stable Ramp identifier. Stored on the Vendor as
	// the `compliance.miloapis.com/ramp-vendor-id` annotation and used as
	// the lookup key on subsequent syncs.
	RampVendorID string

	// DisplayName is the vendor's public-facing name.
	DisplayName string

	// LegalEntity is the registered legal name. Ramp doesn't distinguish
	// it from the display name, so we seed both with the same value and
	// operators can split them before activating the compliance profile.
	LegalEntity string

	// CountryOfIncorporation is the ISO 3166-1 alpha-2 country code. Ramp's
	// accounting vendors endpoint doesn't expose a country today, so the
	// mapper emits "UN" — clearly-not-a-real-country and required to keep
	// the compliance CRD validator happy until an operator fixes it during
	// review.
	CountryOfIncorporation string
}

// MapVendor turns a Ramp accounting vendor into a MappedVendor. Returns
// false when the vendor is not eligible for import (e.g. blank name).
func MapVendor(v AccountingVendor) (MappedVendor, bool) {
	name := strings.TrimSpace(v.Name)
	if name == "" || v.ID == "" {
		return MappedVendor{}, false
	}

	return MappedVendor{
		Name:                   BuildResourceName(name, v.ID),
		RampVendorID:           v.ID,
		DisplayName:            name,
		LegalEntity:            name,
		CountryOfIncorporation: "UN",
	}, true
}

// slugifyPattern matches one or more characters that aren't a lowercase
// alphanumeric and replaces each run with a single hyphen. The result is
// then trimmed of leading/trailing hyphens.
var slugifyPattern = regexp.MustCompile(`[^a-z0-9]+`)

// BuildResourceName produces a stable, RFC 1123-compliant Kubernetes
// resource name from a Ramp vendor name plus a short hash of its ID.
// The hash suffix keeps collisions deterministic when two vendors share a
// name and means re-runs always derive the same K8s name for the same
// Ramp vendor.
func BuildResourceName(displayName, vendorID string) string {
	slug := strings.ToLower(displayName)
	slug = slugifyPattern.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "ramp-vendor"
	}
	// Cap the slug so the suffixed name still fits inside the 63-char
	// DNS label limit (54 + "-" + 8-hex = 63).
	if len(slug) > 54 {
		slug = strings.TrimRight(slug[:54], "-")
	}

	sum := sha1.Sum([]byte(vendorID)) //nolint:gosec
	suffix := hex.EncodeToString(sum[:])[:8]
	return fmt.Sprintf("%s-%s", slug, suffix)
}
