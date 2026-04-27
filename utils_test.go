package main

import (
	"encoding/base64"
	"net/http"
	"testing"
)

func TestTokenAuthHeader(t *testing.T) {
	tests := []struct {
		name  string
		token string
		want  string
	}{
		{
			name:  "encodes non-empty token",
			token: "mytoken",
			want:  "Basic " + base64.StdEncoding.EncodeToString([]byte("token:mytoken")),
		},
		{
			name:  "encodes empty token",
			token: "",
			want:  "Basic " + base64.StdEncoding.EncodeToString([]byte("token:")),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TokenAuthHeader(tt.token)
			if got != tt.want {
				t.Errorf("TokenAuthHeader(%q) = %q, want %q", tt.token, got, tt.want)
			}
		})
	}
}

func TestIsNavPackage(t *testing.T) {
	tests := []struct {
		name    string
		groupID string
		want    bool
	}{
		{"com.github.navikt prefix", "com.github.navikt.something", true},
		{"no.nav prefix", "no.nav.foo", true},
		{"no.stelvio prefix", "no.stelvio.bar", true},
		{"apache commons - false", "org.apache.commons", false},
		{"com.github.other - false", "com.github.other", false},
		{"empty - false", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsNavPackage(tt.groupID)
			if got != tt.want {
				t.Errorf("IsNavPackage(%q) = %v, want %v", tt.groupID, got, tt.want)
			}
		})
	}
}

func TestIsMavenMetadataXml(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{"path ending in /maven-metadata.xml", "com/example/foo/maven-metadata.xml", true},
		{"bare maven-metadata.xml", "maven-metadata.xml", true},
		{"jar file - false", "com/example/foo/artifact-1.0.jar", false},
		{"sha1 file - false", "maven-metadata.xml.sha1", false},
		{"xmlx suffix - false", "path/to/maven-metadata.xmlx", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsMavenMetadataXml(tt.path)
			if got != tt.want {
				t.Errorf("IsMavenMetadataXml(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestModifiedHeadersWithAuth(t *testing.T) {
	t.Run("sets authorization and removes host, preserves other headers", func(t *testing.T) {
		orig := http.Header{
			"Authorization": {"old-auth"},
			"Host":          {"example.com"},
			"X-Custom":      {"custom-value"},
		}
		token := "secret"
		got := ModifiedHeadersWithAuth(orig, token)
		if got.Get("Authorization") != TokenAuthHeader(token) {
			t.Errorf("Authorization = %q, want %q", got.Get("Authorization"), TokenAuthHeader(token))
		}
		if got.Get("Host") != "" {
			t.Errorf("Host should be deleted, got %q", got.Get("Host"))
		}
		if got.Get("X-Custom") != "custom-value" {
			t.Errorf("X-Custom = %q, want %q", got.Get("X-Custom"), "custom-value")
		}
	})

	t.Run("does not mutate original headers", func(t *testing.T) {
		orig := http.Header{
			"Authorization": {"original-auth"},
			"Host":          {"original-host"},
		}
		_ = ModifiedHeadersWithAuth(orig, "new-token")
		if orig.Get("Authorization") != "original-auth" {
			t.Errorf("original Authorization mutated: got %q", orig.Get("Authorization"))
		}
		if orig.Get("Host") != "original-host" {
			t.Errorf("original Host mutated: got %q", orig.Get("Host"))
		}
	})
}

func TestParsePathAsArtifact(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		wantErr   bool
		wantGroup string
		wantArtID string
		wantVer   string
		wantFile  string
	}{
		{
			name:      "standard jar",
			path:      "no/nav/foo/bar/1.0.0/bar-1.0.0.jar",
			wantGroup: "no.nav.foo",
			wantArtID: "bar",
			wantVer:   "1.0.0",
			wantFile:  "bar-1.0.0.jar",
		},
		{
			name:      "standard pom",
			path:      "com/github/navikt/mylib/2.3.4/mylib-2.3.4.pom",
			wantGroup: "com.github.navikt",
			wantArtID: "mylib",
			wantVer:   "2.3.4",
			wantFile:  "mylib-2.3.4.pom",
		},
		{
			name:      "deep groupId with complex version",
			path:      "no/nav/tjenestespesifikasjoner/aktorid-jaxws/1.2019.12.18-12.22-ce897c4eb2c1/aktorid-jaxws-1.2019.12.18-12.22-ce897c4eb2c1.pom",
			wantGroup: "no.nav.tjenestespesifikasjoner",
			wantArtID: "aktorid-jaxws",
			wantVer:   "1.2019.12.18-12.22-ce897c4eb2c1",
			wantFile:  "aktorid-jaxws-1.2019.12.18-12.22-ce897c4eb2c1.pom",
		},
		{
			name:      "maven-metadata.xml path has empty version",
			path:      "no/nav/foo/bar/maven-metadata.xml",
			wantGroup: "no.nav.foo",
			wantArtID: "bar",
			wantVer:   "",
			wantFile:  "maven-metadata.xml",
		},
		{
			name:    "throws for a/b (length 3)",
			path:    "a/b",
			wantErr: true,
		},
		{
			name:    "throws for abc (length 3)",
			path:    "abc",
			wantErr: true,
		},
		{
			name:    "does NOT throw for abcd (length 4) - quirk preserved",
			path:    "abcd",
			wantErr: false,
		},
		{
			name:    "does NOT throw for abcdef (length 6)",
			path:    "abcdef",
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParsePathAsArtifact(tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParsePathAsArtifact(%q) error = %v, wantErr %v", tt.path, err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}
			if tt.wantGroup == "" && tt.wantArtID == "" && tt.wantVer == "" && tt.wantFile == "" {
				return
			}
			if got.GroupID != tt.wantGroup {
				t.Errorf("GroupID = %q, want %q", got.GroupID, tt.wantGroup)
			}
			if got.ArtifactID != tt.wantArtID {
				t.Errorf("ArtifactID = %q, want %q", got.ArtifactID, tt.wantArtID)
			}
			if got.Version != tt.wantVer {
				t.Errorf("Version = %q, want %q", got.Version, tt.wantVer)
			}
			if got.File != tt.wantFile {
				t.Errorf("File = %q, want %q", got.File, tt.wantFile)
			}
		})
	}
}
