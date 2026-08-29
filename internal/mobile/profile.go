package mobile

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"encoding/xml"
	"fmt"
	"strings"
)

// Certificate is pano's root in the forms devices want it in.
type Certificate struct {
	PEM     []byte
	DER     []byte
	Subject string
	SHA256  string // hex fingerprint of the DER
}

// ParseCertificate reads the root from PEM.
func ParseCertificate(pemBytes []byte) (Certificate, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "CERTIFICATE" {
		return Certificate{}, fmt.Errorf("mobile: CA is not a PEM certificate")
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return Certificate{}, fmt.Errorf("mobile: parse CA: %w", err)
	}
	sum := sha256.Sum256(block.Bytes)
	return Certificate{
		PEM: append([]byte(nil), pemBytes...), DER: block.Bytes, Subject: c.Subject.CommonName,
		SHA256: fmt.Sprintf("%x", sum),
	}, nil
}

// Fingerprint returns the SHA-256 in the colon form Settings apps display.
func (c Certificate) Fingerprint() string {
	var b strings.Builder
	for i := 0; i < len(c.SHA256); i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(strings.ToUpper(c.SHA256[i : i+2]))
	}
	return b.String()
}

// MobileConfig renders an Apple configuration profile that installs the root
// as a trusted certificate payload. iOS shows it as a "Profile Downloaded"
// the user installs in Settings, then enables full trust for under
// General → About → Certificate Trust Settings. The profile carries a
// description that says what it is for and how to remove it, and stable
// UUIDs derived from the certificate so re-downloading replaces rather than
// duplicates it. machine names the Mac in the description.
func MobileConfig(c Certificate, machine string) []byte {
	profileUUID := uuidFrom(c.SHA256, "profile")
	certUUID := uuidFrom(c.SHA256, "cert")
	desc := "Lets pano on " + machine + " decrypt this device's HTTPS traffic while pano is set as its Wi-Fi proxy. " +
		"Remove this profile when you are done: Settings → General → VPN & Device Management → pano CA → Remove."
	b64 := base64.StdEncoding.EncodeToString(c.DER)
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>PayloadContent</key>
	<array>
		<dict>
			<key>PayloadCertificateFileName</key>
			<string>pano-ca.cer</string>
			<key>PayloadContent</key>
			<data>
`)
	for i := 0; i < len(b64); i += 64 {
		end := min(i+64, len(b64))
		b.WriteString("\t\t\t" + b64[i:end] + "\n")
	}
	b.WriteString(`			</data>
			<key>PayloadDescription</key>
			<string>` + esc("pano root certificate ("+c.Subject+")") + `</string>
			<key>PayloadDisplayName</key>
			<string>` + esc(c.Subject) + `</string>
			<key>PayloadIdentifier</key>
			<string>internal.pano.ca.root</string>
			<key>PayloadType</key>
			<string>com.apple.security.root</string>
			<key>PayloadUUID</key>
			<string>` + certUUID + `</string>
			<key>PayloadVersion</key>
			<integer>1</integer>
		</dict>
	</array>
	<key>PayloadDescription</key>
	<string>` + esc(desc) + `</string>
	<key>PayloadDisplayName</key>
	<string>pano CA</string>
	<key>PayloadIdentifier</key>
	<string>internal.pano.ca</string>
	<key>PayloadOrganization</key>
	<string>pano</string>
	<key>PayloadRemovalDisallowed</key>
	<false/>
	<key>PayloadType</key>
	<string>Configuration</string>
	<key>PayloadUUID</key>
	<string>` + profileUUID + `</string>
	<key>PayloadVersion</key>
	<integer>1</integer>
</dict>
</plist>
`)
	return b.Bytes()
}

func esc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// uuidFrom derives a stable RFC 4122-shaped UUID from the certificate.
func uuidFrom(fingerprint, salt string) string {
	h := sha256.Sum256([]byte(fingerprint + "/" + salt))
	h[6] = (h[6] & 0x0f) | 0x50 // version 5-ish
	h[8] = (h[8] & 0x3f) | 0x80 // variant
	return strings.ToUpper(fmt.Sprintf("%x-%x-%x-%x-%x", h[0:4], h[4:6], h[6:8], h[8:10], h[10:16]))
}
