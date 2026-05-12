// SPDX-License-Identifier: AGPL-3.0-only

// Package syncer drives the periodic Ramp -> Milo Vendor sync. It owns
// the timer loop, list/match logic, and upsert behaviour. By design it
// touches the compliance schema only through unstructured.Unstructured so
// the provider doesn't have to track every Vendor CRD change in lockstep.
package syncer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"go.miloapis.com/ramp-provider/internal/ramp"
)

const (
	// LabelImportedFrom marks Vendor records that were created by an
	// external provider. The value identifies which provider; this one
	// always writes "ramp". Mirrors annotationImportedFrom so List can
	// use a label selector to narrow the search space, since annotations
	// aren't selectable.
	LabelImportedFrom = "compliance.miloapis.com/imported-from"

	// annotationImportedFrom records the originating provider for a
	// Vendor.
	annotationImportedFrom = "compliance.miloapis.com/imported-from"

	// annotationRampVendorID records the stable Ramp identifier on a
	// Vendor so subsequent syncs can find the existing CR without
	// relying on display name.
	annotationRampVendorID = "compliance.miloapis.com/ramp-vendor-id"

	// sourceRamp is the value stored on annotationImportedFrom for
	// Vendors this provider creates.
	sourceRamp = "ramp"

	// phaseActive is the lifecycle state the syncer refuses to overwrite.
	phaseActive = "Active"
)

// vendorGVK identifies compliance.miloapis.com/v1alpha1 Vendor. It is
// used to build unstructured objects with the right TypeMeta when
// listing and writing through the controller-runtime client.
var vendorGVK = schema.GroupVersionKind{
	Group:   "compliance.miloapis.com",
	Version: "v1alpha1",
	Kind:    "Vendor",
}

// Config configures the periodic Ramp sync.
type Config struct {
	// Client writes Vendor CRs to the Milo aggregated API server. It
	// must already have the unstructured scheme registered (the default
	// controller-runtime client does).
	Client client.Client

	// Ramp is the upstream API client. The syncer borrows the existing
	// active-only listing behaviour from ramp.Client.
	Ramp *ramp.Client

	// Interval is how often the syncer wakes up to run a pass. The first
	// pass fires immediately on Start.
	Interval time.Duration

	// Log is the logger to use for sync events.
	Log logr.Logger

	// NowFunc lets tests freeze time. Defaults to time.Now.
	NowFunc func() time.Time
}

// Syncer implements manager.Runnable so it can sit alongside leader
// election under the controller-runtime manager and start/stop with the
// rest of the controller's lifecycle.
type Syncer struct {
	cfg Config
}

// New validates the Config and returns a runnable Syncer.
func New(cfg Config) (*Syncer, error) {
	if cfg.Client == nil {
		return nil, errors.New("syncer: Client is required")
	}
	if cfg.Ramp == nil {
		return nil, errors.New("syncer: Ramp client is required")
	}
	if cfg.Interval <= 0 {
		return nil, errors.New("syncer: Interval must be positive")
	}
	if cfg.NowFunc == nil {
		cfg.NowFunc = time.Now
	}
	return &Syncer{cfg: cfg}, nil
}

// NeedLeaderElection ensures only the leader pod runs the periodic sync,
// since two pods racing to write the same Vendor CRs would just churn
// resource versions.
func (s *Syncer) NeedLeaderElection() bool { return true }

// Start runs the sync loop until ctx is cancelled. Implements
// manager.Runnable.
func (s *Syncer) Start(ctx context.Context) error {
	log := s.cfg.Log.WithName("ramp-syncer")
	log.Info("starting ramp sync loop", "interval", s.cfg.Interval.String())

	// Fire one pass immediately so an operator restart doesn't have to
	// wait a whole interval for the first sync.
	s.runOnce(ctx, log)

	ticker := time.NewTicker(s.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info("ramp sync loop stopping")
			return nil
		case <-ticker.C:
			s.runOnce(ctx, log)
		}
	}
}

// SyncResult records the outcome of a single sync pass. Exposed so tests
// and callers can assert on counts; the Start loop logs it.
type SyncResult struct {
	Imported []string
	Skipped  []string
}

func (s *Syncer) runOnce(ctx context.Context, log logr.Logger) {
	start := s.cfg.NowFunc()
	result, err := s.Sync(ctx)
	dur := s.cfg.NowFunc().Sub(start)
	if err != nil {
		log.Error(err, "ramp sync failed", "duration", dur.String())
		return
	}
	log.Info("ramp sync completed",
		"duration", dur.String(),
		"imported", len(result.Imported),
		"skippedActive", len(result.Skipped),
	)
}

// Sync runs one pass and returns a SyncResult. Exposed for unit tests.
func (s *Syncer) Sync(ctx context.Context) (SyncResult, error) {
	rampVendors, err := s.cfg.Ramp.ListActiveVendors(ctx)
	if err != nil {
		return SyncResult{}, fmt.Errorf("listing ramp vendors: %w", err)
	}

	existing, err := s.listImported(ctx)
	if err != nil {
		return SyncResult{}, fmt.Errorf("listing imported vendors: %w", err)
	}

	imported := make([]string, 0, len(rampVendors))
	skipped := make([]string, 0)

	for _, v := range rampVendors {
		mapped, ok := ramp.MapVendor(v)
		if !ok {
			continue
		}
		current, found := existing[mapped.RampVendorID]
		if !found {
			created, err := s.createVendor(ctx, mapped)
			if err != nil {
				return SyncResult{}, fmt.Errorf("creating vendor %q: %w", mapped.Name, err)
			}
			imported = append(imported, created)
			continue
		}
		phase, _, _ := unstructured.NestedString(current.Object, "spec", "complianceProfile", "phase")
		if phase == phaseActive {
			skipped = append(skipped, current.GetName())
			continue
		}
		updated, err := s.updateVendor(ctx, current, mapped)
		if err != nil {
			return SyncResult{}, fmt.Errorf("updating vendor %q: %w", current.GetName(), err)
		}
		imported = append(imported, updated)
	}

	return SyncResult{Imported: imported, Skipped: skipped}, nil
}

// listImported returns the Vendor CRs marked as imported by this provider,
// keyed by Ramp vendor ID.
func (s *Syncer) listImported(ctx context.Context) (map[string]*unstructured.Unstructured, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   vendorGVK.Group,
		Version: vendorGVK.Version,
		Kind:    vendorGVK.Kind + "List",
	})
	if err := s.cfg.Client.List(ctx, list, client.MatchingLabels{LabelImportedFrom: sourceRamp}); err != nil {
		return nil, err
	}

	out := make(map[string]*unstructured.Unstructured, len(list.Items))
	for i := range list.Items {
		item := &list.Items[i]
		id := item.GetAnnotations()[annotationRampVendorID]
		if id == "" {
			continue
		}
		out[id] = item
	}
	return out, nil
}

func (s *Syncer) createVendor(ctx context.Context, mapped ramp.MappedVendor) (string, error) {
	vendor := &unstructured.Unstructured{}
	vendor.SetGroupVersionKind(vendorGVK)
	vendor.SetName(mapped.Name)
	vendor.SetLabels(map[string]string{LabelImportedFrom: sourceRamp})
	vendor.SetAnnotations(map[string]string{
		annotationImportedFrom: sourceRamp,
		annotationRampVendorID: mapped.RampVendorID,
	})

	spec := map[string]any{
		"displayName":            mapped.DisplayName,
		"legalEntity":            mapped.LegalEntity,
		"countryOfIncorporation": mapped.CountryOfIncorporation,
	}
	if err := unstructured.SetNestedField(vendor.Object, spec, "spec"); err != nil {
		return "", fmt.Errorf("setting spec: %w", err)
	}

	if err := s.cfg.Client.Create(ctx, vendor); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Possible if a previous run started the create and then the
			// pod crashed before recording it; fall through to a
			// refresh-style patch on next pass.
			return mapped.Name, nil
		}
		return "", err
	}
	return mapped.Name, nil
}

func (s *Syncer) updateVendor(ctx context.Context, current *unstructured.Unstructured, mapped ramp.MappedVendor) (string, error) {
	original := current.DeepCopy()

	// Refresh the adapter-owned identity fields, preserving anything the
	// operator may have already filled in.
	if err := unstructured.SetNestedField(current.Object, mapped.DisplayName, "spec", "displayName"); err != nil {
		return "", err
	}
	if err := unstructured.SetNestedField(current.Object, mapped.LegalEntity, "spec", "legalEntity"); err != nil {
		return "", err
	}
	existingCountry, _, _ := unstructured.NestedString(current.Object, "spec", "countryOfIncorporation")
	if existingCountry == "" || existingCountry == "UN" {
		if err := unstructured.SetNestedField(current.Object, mapped.CountryOfIncorporation, "spec", "countryOfIncorporation"); err != nil {
			return "", err
		}
	}

	annotations := current.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[annotationImportedFrom] = sourceRamp
	annotations[annotationRampVendorID] = mapped.RampVendorID
	current.SetAnnotations(annotations)

	labels := current.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[LabelImportedFrom] = sourceRamp
	current.SetLabels(labels)

	if err := s.cfg.Client.Patch(ctx, current, client.MergeFrom(original)); err != nil {
		return "", err
	}
	return current.GetName(), nil
}

// Ensure compile-time conformance with manager.Runnable + the leader
// election hint.
var (
	_ manager.Runnable               = (*Syncer)(nil)
	_ manager.LeaderElectionRunnable = (*Syncer)(nil)
)
