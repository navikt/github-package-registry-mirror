package main

import (
	"encoding/base64"
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
			got := IsMavenMetadataXML(tt.path)
			if got != tt.want {
				t.Errorf("IsMavenMetadataXml(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestModifiedHeadersWithAuth(t *testing.T) {
	t.Run("sets authorization header", func(t *testing.T) {
		token := "secret"
		got := ModifiedHeadersWithAuth(token)
		if got.Get("Authorization") != TokenAuthHeader(token) {
			t.Errorf("Authorization = %q, want %q", got.Get("Authorization"), TokenAuthHeader(token))
		}
	})

	t.Run("contains only authorization header", func(t *testing.T) {
		got := ModifiedHeadersWithAuth("token")
		if len(got) != 1 {
			t.Errorf("header count = %d, want 1, headers: %v", len(got), got)
		}
	})
}

func TestIsValidMavenCoordinate(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"no.nav.foo", true},
		{"com.github.navikt", true},
		{"my-artifact", true},
		{"version_1.0", true},
		{"", false},
		{"foo\"bar", false},
		{"foo bar", false},
		{"foo/bar", false},
		{"foo\\bar", false},
		{`foo}bar`, false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := IsValidMavenCoordinate(tt.input); got != tt.want {
				t.Errorf("IsValidMavenCoordinate(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestContainsPathTraversal(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{"valid path", "no/nav/foo/bar", false},
		{"dot segment", "no/nav/./bar", true},
		{"dot dot segment", "no/nav/../bar", true},
		{"leading slash creates empty segment", "/no/nav/bar", true},
		{"double slash creates empty segment", "no//nav/bar", true},
		{"empty path", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := containsPathTraversal(tt.path); got != tt.want {
				t.Errorf("containsPathTraversal(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
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
			name:    "throws for a/b (2 segments, need 4)",
			path:    "a/b",
			wantErr: true,
		},
		{
			name:    "throws for abc (1 segment, need 4)",
			path:    "abc",
			wantErr: true,
		},
		{
			name:    "throws for a/b/c (3 segments, need 4)",
			path:    "a/b/c",
			wantErr: true,
		},
		{
			name:      "4 segments is minimum for artifact paths",
			path:      "a/b/c/d",
			wantGroup: "a",
			wantArtID: "b",
			wantVer:   "c",
			wantFile:  "d",
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

func TestIsValidPathSegment(t *testing.T) {
	tests := []struct {
		name    string
		segment string
		want    bool
	}{
		{"simple name", "my-artifact", true},
		{"version with dots", "1.2.3", true},
		{"version with plus", "1.0.0+build", true},
		{"underscore", "my_artifact", true},
		{"uppercase", "MyArtifact", true},
		{"jar extension", "artifact-1.0.jar", true},
		{"pom extension", "artifact-1.0.pom", true},
		{"empty string", "", false},
		{"path traversal dots", "..", true}, // character-valid; containsPathTraversal catches this separately
		{"slash", "a/b", false},
		{"backslash", `a\b`, false},
		{"space", "a b", false},
		{"null byte", "a\x00b", false},
		{"colon", "a:b", false},
		{"semicolon", "a;b", false},
		{"question mark", "a?b", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsValidPathSegment(tt.segment)
			if got != tt.want {
				t.Errorf("IsValidPathSegment(%q) = %v, want %v", tt.segment, got, tt.want)
			}
		})
	}
}
