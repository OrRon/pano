package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) Paths {
	t.Helper()
	p := Paths{Dir: t.TempDir()}
	if err := os.WriteFile(p.ConfigFile(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDefaultDecrypt(t *testing.T) {
	cfg := Default()
	if cfg.Decrypt.Mode != "all" || len(cfg.Decrypt.Only) != 0 || len(cfg.Decrypt.Never) != len(DefaultNever) {
		t.Fatalf("default decrypt: %+v", cfg.Decrypt)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestBypassMigratesToNever(t *testing.T) {
	p := writeConfig(t, "[proxy]\nport = 9091\nbypass = [\"*.bank.example\", \"pinned.example\"]\n")
	cfg, warnings, err := LoadWithWarnings(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.Decrypt.Never, ",") != "*.bank.example,pinned.example" || cfg.Decrypt.Mode != "all" {
		t.Fatalf("migrated: %+v", cfg.Decrypt)
	}
	if len(cfg.Proxy.Bypass) != 0 || len(warnings) != 1 || !strings.Contains(warnings[0], "deprecated") {
		t.Fatalf("bypass=%v warnings=%v", cfg.Proxy.Bypass, warnings)
	}
	// Saving writes [decrypt] and drops the old key.
	if err := Save(p, cfg); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p.ConfigFile())
	if strings.Contains(string(b), "bypass") || !strings.Contains(string(b), "[decrypt]") {
		t.Fatalf("saved config:\n%s", b)
	}
}

func TestBypassIgnoredWhenDecryptPresent(t *testing.T) {
	p := writeConfig(t, "[proxy]\nbypass = [\"old.example\"]\n\n[decrypt]\nmode = \"only\"\nonly = [\"api.example\"]\nnever = [\"new.example\"]\n")
	cfg, warnings, err := LoadWithWarnings(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Decrypt.Mode != "only" || strings.Join(cfg.Decrypt.Never, ",") != "new.example" || strings.Join(cfg.Decrypt.Only, ",") != "api.example" {
		t.Fatalf("decrypt: %+v", cfg.Decrypt)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "ignored") {
		t.Fatalf("warnings: %v", warnings)
	}
}

func TestDecryptValidate(t *testing.T) {
	for _, body := range []string{
		"[decrypt]\nmode = \"maybe\"\n",
		"[decrypt]\nmode = \"all\"\nnever = [\"\"]\n",
		"[decrypt]\nmode = \"all\"\nonly = [\"two words\"]\n",
	} {
		p := writeConfig(t, body)
		if _, err := Load(p); err == nil {
			t.Errorf("expected validation error for %q", body)
		}
	}
	// Missing file → defaults, no warnings.
	cfg, warnings, err := LoadWithWarnings(Paths{Dir: filepath.Join(t.TempDir(), "none")})
	if err != nil || len(warnings) != 0 || cfg.Decrypt.Mode != "all" {
		t.Fatalf("missing file: %+v %v %v", cfg.Decrypt, warnings, err)
	}
}
