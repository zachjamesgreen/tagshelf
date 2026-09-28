package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test_config.yaml")
	yamlContent := `
source: "C:\\MusicSource"
destination: "C:\\MusicDest"
untagged: "C:\\MusicUntagged"
other: "C:\\MusicOther"
compilations: "Compilations"
workers: 4
dry_run: false
`
	if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Load without CLI flags override
	cfg, err := Load(configPath, Overrides{})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.Source != "C:\\MusicSource" || cfg.Destination != "C:\\MusicDest" || cfg.Other != "C:\\MusicOther" ||
		cfg.Compilations != "Compilations" || cfg.Workers != 4 {
		t.Errorf("Config parsing failed: %+v", cfg)
	}

	// CLI flags override config file
	dryRun := true
	cfgOverride, err := Load(configPath, Overrides{Source: "D:\\Overridden", Other: "D:\\Other", Compilations: "VA", Workers: 16, DryRun: &dryRun})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfgOverride.Source != "D:\\Overridden" {
		t.Errorf("Expected source overridden to D:\\Overridden, got %s", cfgOverride.Source)
	}
	if cfgOverride.Other != "D:\\Other" {
		t.Errorf("Expected other overridden to D:\\Other, got %s", cfgOverride.Other)
	}
	if cfgOverride.Compilations != "VA" {
		t.Errorf("Expected compilations overridden to VA, got %s", cfgOverride.Compilations)
	}
	if cfgOverride.Workers != 16 {
		t.Errorf("Expected workers overridden to 16, got %d", cfgOverride.Workers)
	}
	if !cfgOverride.DryRun {
		t.Errorf("Expected dry_run overridden to true")
	}
}

func TestLoadConfigVerbose(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "verbose_config.yaml")
	yamlContent := `
source: "C:\\MusicSource"
destination: "C:\\MusicDest"
verbose: true
dry_run: true
`
	if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(configPath, Overrides{})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if !cfg.Verbose || !cfg.DryRun {
		t.Errorf("Expected verbose and dry_run true from YAML, got %+v", cfg)
	}

	// An explicit -verbose=false / -dry-run=false must be able to turn the
	// config file's setting back off.
	off := false
	cfgOff, err := Load(configPath, Overrides{Verbose: &off, DryRun: &off})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfgOff.Verbose {
		t.Errorf("Expected -verbose=false to override verbose: true from YAML")
	}
	if cfgOff.DryRun {
		t.Errorf("Expected -dry-run=false to override dry_run: true from YAML")
	}

	on := true
	cfgOn, err := Load(configPath, Overrides{Verbose: &on})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if !cfgOn.Verbose {
		t.Errorf("Expected verbose=true with CLI flag")
	}
}

func TestLoadConfigMissingExplicitFileIsFatal(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does_not_exist.yaml")
	if _, err := Load(missing, Overrides{Source: "C:\\Src", Destination: "C:\\Dst"}); err == nil {
		t.Errorf("Expected an error for an explicitly requested config file that cannot be read")
	}
}

func TestLoadConfigMalformedExplicitFileIsFatal(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "broken.yaml")
	if err := os.WriteFile(configPath, []byte("workers: not-a-number\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(configPath, Overrides{}); err == nil {
		t.Errorf("Expected an error for an explicitly requested config file that cannot be parsed")
	}
}

func TestLoadConfigForeignDiscoveredFileIsIgnored(t *testing.T) {
	tmpDir := t.TempDir()
	// An unrelated config.yaml belonging to some other program.
	if err := os.WriteFile(filepath.Join(tmpDir, DefaultConfigName), []byte("workers: [1, 2, 3]\n"), 0644); err != nil {
		t.Fatal(err)
	}

	prevDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(prevDir); err != nil {
			t.Fatal(err)
		}
	}()

	cfg, err := Load("", Overrides{Source: "C:\\Src", Destination: "C:\\Dst"})
	if err != nil {
		t.Fatalf("A foreign config.yaml must not abort a flag-driven run: %v", err)
	}
	if cfg.Source != "C:\\Src" || cfg.Destination != "C:\\Dst" {
		t.Errorf("Expected the CLI flags to stand on their own, got %+v", cfg)
	}
}

func TestWithDefaults(t *testing.T) {
	cfg := Config{Destination: filepath.Join("C:", "Out")}.WithDefaults()
	if cfg.Workers < 1 {
		t.Errorf("Expected WithDefaults to pick at least one worker, got %d", cfg.Workers)
	}
	expectedUntagged := filepath.Join("C:", "Out", "_Untagged")
	if cfg.Untagged != expectedUntagged {
		t.Errorf("Expected untagged %q, got %q", expectedUntagged, cfg.Untagged)
	}
	expectedOther := filepath.Join("C:", "Out", "_Other")
	if cfg.Other != expectedOther {
		t.Errorf("Expected other %q, got %q", expectedOther, cfg.Other)
	}
	if cfg.Compilations != "Various Artists" {
		t.Errorf("Expected compilations to default to Various Artists, got %q", cfg.Compilations)
	}

	explicit := Config{Destination: "d", Untagged: "u", Other: "o", Compilations: "c", Workers: 3}.WithDefaults()
	if explicit.Untagged != "u" || explicit.Other != "o" || explicit.Compilations != "c" || explicit.Workers != 3 {
		t.Errorf("WithDefaults must not overwrite explicit values: %+v", explicit)
	}
}

func TestLoadConfigMusicBrainzContact(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "mb_config.yaml")
	if err := os.WriteFile(configPath, []byte("musicbrainz_contact: \"me@example.com\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(configPath, Overrides{})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.MusicBrainzContact != "me@example.com" {
		t.Errorf("MusicBrainzContact = %q, want me@example.com", cfg.MusicBrainzContact)
	}
}
