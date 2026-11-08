package domain

// A Source is an external system we ingest from (Reddit, ChatGPT, …). The domain
// knows what a source is and which event types it yields; how one is parsed
// (readers, archive members, extractors) is the provider SPI in
// internal/provider, implemented by the adapters under /providers.

// Mode is an ingestion method a source supports.
type Mode string

const (
	ModeAPIPull Mode = "api_pull"
	ModeOAuth   Mode = "oauth"
	ModeBundle  Mode = "bundle"
	ModeWebhook Mode = "webhook"
)

// Maturity says how far a source's provider has been proven.
type Maturity string

const (
	// MaturityVerified means developed or checked against a real export.
	MaturityVerified Maturity = "verified"
	// MaturityExperimental means written from the documented format and tested
	// with synthetic fixtures only — expect it to need adjusting on first
	// contact with a real export (what it does not recognise is still kept,
	// as <source>.unknown).
	MaturityExperimental Maturity = "experimental"
)

// Descriptor is static metadata about a source.
type Descriptor struct {
	ID             string
	DisplayName    string
	IngestionModes []Mode
	EventTypes     []TypeDef
	// Maturity is MaturityVerified once the provider has met a real export;
	// empty means experimental.
	Maturity Maturity
}

// Verified reports whether the source's provider has met a real export.
func (d Descriptor) Verified() bool { return d.Maturity == MaturityVerified }
