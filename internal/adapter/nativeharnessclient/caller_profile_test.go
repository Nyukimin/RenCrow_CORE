package nativeharnessclient

import (
	"errors"
	"strings"
	"testing"
)

const matchingHarnessConfig = `{
  "config_version": "rencrow-harness-config/v1",
  "caller": {
    "principal": "core:local",
    "default_origin": "automation",
    "readable_session_owners": ["core:local"],
    "controllable_session_owners": ["core:local"],
    "relay_issuers": [
      {"issuer": "other:issuer", "key_id": "other-key", "audience": "core:local", "key_file": "/k/other"},
      {"issuer": "core:fixture", "key_id": "fixture-key-1", "audience": "core:local", "key_file": "/k/core"}
    ]
  },
  "workspaces": []
}`

func TestCheckHarnessCallerProfileAcceptsMatchingConfig(t *testing.T) {
	// Arrange
	settings := fixtureSettings()

	// Act
	err := CheckHarnessCallerProfile(settings, []byte(matchingHarnessConfig))

	// Assert
	if err != nil {
		t.Fatalf("CheckHarnessCallerProfile: %v", err)
	}
}

func TestCheckHarnessCallerProfileRejectsEveryMismatch(t *testing.T) {
	replace := func(old, replacement string) string {
		if !strings.Contains(matchingHarnessConfig, old) {
			t.Fatalf("test config does not contain %q", old)
		}
		return strings.Replace(matchingHarnessConfig, old, replacement, 1)
	}
	cases := map[string]struct {
		settings IssuerSettings
		config   string
	}{
		"not json":                   {fixtureSettings(), "caller: {}"},
		"empty":                      {fixtureSettings(), ""},
		"trailing value":             {fixtureSettings(), matchingHarnessConfig + " {}"},
		"top level array":            {fixtureSettings(), "[]"},
		"no caller":                  {fixtureSettings(), `{"workspaces": []}`},
		"caller is null":             {fixtureSettings(), `{"caller": null}`},
		"principal is the user":      {fixtureSettings(), replace(`"principal": "core:local"`, `"principal": "user:ren"`)},
		"principal missing":          {fixtureSettings(), replace(`"principal": "core:local",`, ``)},
		"default origin human":       {fixtureSettings(), replace(`"default_origin": "automation"`, `"default_origin": "human"`)},
		"default origin missing":     {fixtureSettings(), replace(`"default_origin": "automation",`, ``)},
		"relay issuers empty":        {fixtureSettings(), `{"caller": {"principal": "core:local", "default_origin": "automation", "relay_issuers": []}}`},
		"relay issuers missing":      {fixtureSettings(), `{"caller": {"principal": "core:local", "default_origin": "automation"}}`},
		"issuer differs":             {IssuerSettings{Issuer: "core:other", KeyID: "fixture-key-1", Audience: "core:local", TTLSeconds: 300}, matchingHarnessConfig},
		"key id differs":             {IssuerSettings{Issuer: "core:fixture", KeyID: "fixture-key-2", Audience: "core:local", TTLSeconds: 300}, matchingHarnessConfig},
		"allowlist audience differs": {fixtureSettings(), replace(`"issuer": "core:fixture", "key_id": "fixture-key-1", "audience": "core:local"`, `"issuer": "core:fixture", "key_id": "fixture-key-1", "audience": "core:remote"`)},
		"audience is not the principal": {
			IssuerSettings{Issuer: "core:fixture", KeyID: "fixture-key-1", Audience: "core:remote", TTLSeconds: 300},
			replace(`"audience": "core:local", "key_file": "/k/core"`, `"audience": "core:remote", "key_file": "/k/core"`),
		},
		"case differs":     {IssuerSettings{Issuer: "Core:fixture", KeyID: "fixture-key-1", Audience: "core:local", TTLSeconds: 300}, matchingHarnessConfig},
		"invalid settings": {IssuerSettings{}, matchingHarnessConfig},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := CheckHarnessCallerProfile(tc.settings, []byte(tc.config))
			if !errors.Is(err, ErrHarnessCallerProfile) && !errors.Is(err, ErrInvalidIssuerSettings) {
				t.Fatalf("error = %v, want ErrHarnessCallerProfile or ErrInvalidIssuerSettings", err)
			}
		})
	}
}

func TestCheckHarnessCallerProfileErrorNeverEchoesTheConfigText(t *testing.T) {
	const marker = "marker-secret-path-value"
	config := strings.Replace(matchingHarnessConfig, "/k/core", "/"+marker, 1)
	config = strings.Replace(config, `"principal": "core:local"`, `"principal": "user:ren"`, 1)
	err := CheckHarnessCallerProfile(fixtureSettings(), []byte(config))
	if err == nil {
		t.Fatalf("mismatching config accepted")
	}
	if strings.Contains(err.Error(), marker) {
		t.Fatalf("error echoes config text: %v", err)
	}
}
