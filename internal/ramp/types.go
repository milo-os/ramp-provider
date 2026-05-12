// SPDX-License-Identifier: AGPL-3.0-only

// Package ramp implements the Ramp accounting-vendor adapter for the
// compliance service. It fetches vendor records from
// https://api.ramp.com/v1/accounting/vendors using OAuth2
// client_credentials and exposes them as a minimal, stable struct surface
// the controller can map into compliance.miloapis.com/v1alpha1 Vendor
// resources.
package ramp

// AccountingVendor is the subset of the Ramp `ApiAccountingVendorResource`
// schema we currently consume. We deliberately keep this struct narrow:
// every field we map needs a clear `omitempty` and a `null` story for the
// adapter to be useful, and Ramp's full schema is large.
//
// Fields are conservatively typed as strings; Ramp's API documents these
// as nullable strings and the adapter treats empty values as "unknown" on
// the Vendor side. Add fields here (and to mapping.go) as we learn what
// Ramp actually returns for our tenants.
type AccountingVendor struct {
	// ID is the stable Ramp identifier for the vendor. Used as the
	// `compliance.miloapis.com/ramp-vendor-id` annotation on imported
	// Vendor resources, which is how subsequent syncs find the
	// previously-imported record.
	ID string `json:"id"`

	// Name is Ramp's display name for the vendor. Maps to Vendor.spec
	// `displayName` and, when no separate legal entity is known, also
	// `legalEntity`.
	Name string `json:"name"`

	// Code is Ramp's optional internal accounting code. Currently unused
	// by the importer but worth carrying so it can be surfaced in future.
	Code string `json:"code,omitempty"`

	// RemoteID is the foreign key Ramp uses when this vendor mirrors a
	// record from an upstream accounting integration (e.g. NetSuite). Not
	// used by the importer today.
	RemoteID string `json:"remote_id,omitempty"`

	// IsActive indicates whether the vendor is active in Ramp. Inactive
	// vendors are skipped by the importer.
	IsActive bool `json:"is_active"`
}

// ListAccountingVendorsResponse mirrors Ramp's paginated response wrapper.
// The cursor token is exposed under `page.next` in real responses; we
// model it on the outer struct here so the adapter's loop stays simple.
type ListAccountingVendorsResponse struct {
	Data []AccountingVendor `json:"data"`
	Page Page               `json:"page"`
}

// Page captures Ramp's cursor-style pagination metadata. The adapter only
// needs to know whether there's another page to fetch and what cursor to
// pass through.
type Page struct {
	Next string `json:"next,omitempty"`
}
