// Package har exports captured flows as HAR 1.2 documents and imports HAR
// documents produced by pano, browsers (Chrome, Firefox) and other proxies.
//
// Export streams entries one at a time so a large capture never has to be held
// in memory twice. Bodies are looked up on demand through a [BodyFunc], which
// must return decoded bytes (Content-Encoding removed); text bodies are inlined
// as UTF-8, everything else is base64. An optional [Redactor] masks secrets in
// header values and text bodies before they reach the writer.
//
// Each exported entry carries a "_pano" extension object (HAR permits
// underscore-prefixed custom fields) holding the flow id, kind, session, error,
// tags, rule hits and truncation flags. Import restores those fields when
// present and falls back to sensible defaults for HARs produced elsewhere.
//
// The [Log], [Entry] and related types mirror the HAR 1.2 schema and are
// exported so callers can inspect or build documents directly.
package har
