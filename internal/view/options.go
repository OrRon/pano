package view

// View modes accepted by Render.
const (
	ViewSummary   = "summary"
	ViewSchema    = "schema"
	ViewTruncated = "truncated"
	ViewPretty    = "pretty"
	ViewRaw       = "raw"
)

// Defaults and hard limits applied to Options and to the renderers.
const (
	// DefaultMaxBytes is the body budget used when Options.MaxBytes is zero.
	DefaultMaxBytes = 4096
	// MaxBytesCap is the hard upper bound on Options.MaxBytes (1 MiB).
	MaxBytesCap = 1 << 20
	// DefaultStringTruncate is the string length above which summary and
	// schema views elide string values.
	DefaultStringTruncate = 200
	// DefaultArraySample is the number of array elements a summary inspects.
	DefaultArraySample = 3
	// DefaultDecodeLimit bounds the decoded size of an encoded body (8 MiB).
	DefaultDecodeLimit = 8 << 20

	summaryBudget  = 1536 // target size of a summary view
	schemaBudget   = 2048 // target size of a schema view
	schemaMaxDepth = 6    // nesting beyond this renders as "…"
	shortValueLen  = 60   // prefix shown for elided strings
	textPreviewLen = 300  // characters shown for text-ish summaries
	compactJSONLen = 120  // compact JSON snippets are cut here
)

// Options tunes how Render shapes its output. The zero value is usable but
// disables redaction; use DefaultOptions for the recommended defaults.
type Options struct {
	// MaxBytes is the body budget for the truncated, pretty and raw views
	// (default DefaultMaxBytes; values above MaxBytesCap are clamped).
	MaxBytes int
	// StringTruncate elides strings longer than this in summary and schema
	// views (default DefaultStringTruncate).
	StringTruncate int
	// Redact enables secret redaction of the rendered text.
	Redact bool
	// RevealSecrets is a per-call override that disables redaction even when
	// Redact is set.
	RevealSecrets bool
	// ArraySample is the number of leading array elements a summary shows
	// (default DefaultArraySample).
	ArraySample int
}

// DefaultOptions returns the recommended options: a 4 KiB budget, 200-char
// string elision, a three-element array sample and redaction enabled.
func DefaultOptions() Options {
	return Options{
		MaxBytes:       DefaultMaxBytes,
		StringTruncate: DefaultStringTruncate,
		Redact:         true,
		ArraySample:    DefaultArraySample,
	}
}

// normalize fills zero fields with defaults and clamps MaxBytes.
func (o Options) normalize() Options {
	if o.MaxBytes <= 0 {
		o.MaxBytes = DefaultMaxBytes
	}
	if o.MaxBytes > MaxBytesCap {
		o.MaxBytes = MaxBytesCap
	}
	if o.StringTruncate <= 0 {
		o.StringTruncate = DefaultStringTruncate
	}
	if o.ArraySample <= 0 {
		o.ArraySample = DefaultArraySample
	}
	return o
}

// redacting reports whether rendered output must pass through RedactText.
func (o Options) redacting() bool { return o.Redact && !o.RevealSecrets }
