// Package ee defines the extension points where commercial ("enterprise")
// functionality plugs into Lumen.
//
// This package is part of the open-source AGPL distribution and always ships
// with working no-op implementations. The open build is fully functional: quota
// checks pass, metering discards, and entitlements report every open feature as
// enabled. Nothing here is a stub that degrades the community edition.
//
// The commercial module lives in a separate private Go module and is wired in
// by passing its own Hooks to app.Run. There are deliberately no build tags and
// no conditional dependencies, so `go build ./...` always works for everyone.
//
// These interfaces are public on purpose: implement them yourself if you want
// custom quota logic, metering, or audit sinks in your own deployment.
package ee

import "context"

// Edition identifies which build is running.
type Edition string

const (
	// EditionCommunity is the open-source AGPL build.
	EditionCommunity Edition = "community"
	// EditionEnterprise is a licensed commercial build.
	EditionEnterprise Edition = "enterprise"
)

// Entitlements describes what the running build is permitted to do.
type Entitlements struct {
	Edition Edition
	// MaxEventsPerMonth is 0 for unlimited. The community edition is unlimited.
	MaxEventsPerMonth uint64
	// MaxTeams is 0 for unlimited.
	MaxTeams uint32
	// Features is the set of enabled named features.
	Features map[string]bool
	// LicensedTo is a human-readable licensee name, empty for community.
	LicensedTo string
}

// Has reports whether a named feature is enabled.
func (e Entitlements) Has(feature string) bool {
	if e.Features == nil {
		return false
	}
	return e.Features[feature]
}

// Licensor reports the entitlements of the running build.
type Licensor interface {
	Entitlements() Entitlements
}

// QuotaEnforcer decides whether a tenant may write more events.
//
// Implementations must be fast and non-blocking: this sits on the ingest hot
// path and is called once per batch. Return a non-nil error to reject the batch
// with a resource-exhausted response.
type QuotaEnforcer interface {
	CheckQuota(ctx context.Context, teamID string, eventCount int) error
}

// UsageMeter records accepted event volume for billing and reporting.
//
// Implementations must never block the ingest path. Buffer and flush
// asynchronously; drop rather than stall.
type UsageMeter interface {
	RecordUsage(ctx context.Context, teamID string, eventCount int)
}

// AuditEvent is a control-plane action worth recording.
type AuditEvent struct {
	TeamID  string
	Actor   string
	Action  string
	Target  string
	Details map[string]string
}

// AuditSink receives control-plane audit events for export to external systems.
type AuditSink interface {
	Audit(ctx context.Context, event AuditEvent)
}

// Hooks bundles every extension point. A nil field means "not provided"; use
// Normalize to fill nil fields with safe no-op defaults before use.
type Hooks struct {
	Licensor      Licensor
	QuotaEnforcer QuotaEnforcer
	UsageMeter    UsageMeter
	AuditSink     AuditSink
}

// CommunityHooks returns the open-source no-op hook set: unlimited quota, no
// metering, no audit export, community entitlements.
func CommunityHooks() Hooks {
	return Hooks{
		Licensor:      communityLicensor{},
		QuotaEnforcer: allowAllQuota{},
		UsageMeter:    discardMeter{},
		AuditSink:     discardAudit{},
	}
}

// Normalize returns a copy of h with any nil field replaced by its community
// default, so callers never need a nil check.
func (h Hooks) Normalize() Hooks {
	def := CommunityHooks()
	if h.Licensor == nil {
		h.Licensor = def.Licensor
	}
	if h.QuotaEnforcer == nil {
		h.QuotaEnforcer = def.QuotaEnforcer
	}
	if h.UsageMeter == nil {
		h.UsageMeter = def.UsageMeter
	}
	if h.AuditSink == nil {
		h.AuditSink = def.AuditSink
	}
	return h
}

// Community feature names enabled in the open-source build.
const (
	FeatureEventIngest        = "event_ingest"
	FeatureIdentityResolution = "identity_resolution"
	FeatureSessions           = "sessions"
	FeatureEnrichment         = "enrichment"
	FeatureSelfHost           = "self_host"
)

type communityLicensor struct{}

func (communityLicensor) Entitlements() Entitlements {
	return Entitlements{
		Edition:           EditionCommunity,
		MaxEventsPerMonth: 0, // unlimited
		MaxTeams:          0, // unlimited
		Features: map[string]bool{
			FeatureEventIngest:        true,
			FeatureIdentityResolution: true,
			FeatureSessions:           true,
			FeatureEnrichment:         true,
			FeatureSelfHost:           true,
		},
	}
}

type allowAllQuota struct{}

func (allowAllQuota) CheckQuota(context.Context, string, int) error { return nil }

type discardMeter struct{}

func (discardMeter) RecordUsage(context.Context, string, int) {}

type discardAudit struct{}

func (discardAudit) Audit(context.Context, AuditEvent) {}
