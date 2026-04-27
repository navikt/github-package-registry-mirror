package main

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

var (
	validMavenCoordinate = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)
	validPathSegment     = regexp.MustCompile(`^[a-zA-Z0-9._\-+]+$`)
)

// Artifact holds parsed Maven path components.
type Artifact struct {
	GroupID    string
	ArtifactID string
	Version    string
	File       string
}

// TokenAuthHeader returns a Basic auth header value for the given GitHub token.
func TokenAuthHeader(token string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte("token:" + token))
	return "Basic " + encoded
}

// IsNavPackage returns true if groupID starts with a NAV-owned prefix.
func IsNavPackage(groupID string) bool {
	return strings.HasPrefix(groupID, "com.github.navikt") ||
		strings.HasPrefix(groupID, "no.nav") ||
		strings.HasPrefix(groupID, "no.stelvio")
}

// IsMavenMetadataXml returns true if path ends with /maven-metadata.xml or equals maven-metadata.xml.
func IsMavenMetadataXml(path string) bool {
	return strings.HasSuffix(path, "/maven-metadata.xml") || path == "maven-metadata.xml"
}

func IsValidMavenCoordinate(s string) bool {
	return validMavenCoordinate.MatchString(s)
}

func IsValidPathSegment(s string) bool {
	return validPathSegment.MatchString(s)
}

// ModifiedHeadersWithAuth creates fresh headers with Authorization.
func ModifiedHeadersWithAuth(token string) http.Header {
	h := http.Header{}
	h.Set("Authorization", TokenAuthHeader(token))
	return h
}

// ParsePathAsArtifact parses a Maven repository path into its components.
// QUIRK: validates len(path) >= 4 (character count, NOT segment count).
func ParsePathAsArtifact(path string) (Artifact, error) {
	if len(path) < 4 {
		return Artifact{}, fmt.Errorf("not a valid Maven repository path")
	}

	segments := strings.Split(path, "/")
	if strings.HasSuffix(path, "maven-metadata.xml") {
		file := segments[len(segments)-1]
		artifactID := ""
		if len(segments) >= 2 {
			artifactID = segments[len(segments)-2]
		}
		groupParts := []string{}
		if len(segments) > 2 {
			groupParts = segments[:len(segments)-2]
		}
		return Artifact{
			GroupID:    strings.Join(groupParts, "."),
			ArtifactID: artifactID,
			Version:    "",
			File:       file,
		}, nil
	}

	file := segments[len(segments)-1]
	version := ""
	if len(segments) >= 2 {
		version = segments[len(segments)-2]
	}
	artifactID := ""
	if len(segments) >= 3 {
		artifactID = segments[len(segments)-3]
	}
	groupParts := []string{}
	if len(segments) > 3 {
		groupParts = segments[:len(segments)-3]
	}
	return Artifact{
		GroupID:    strings.Join(groupParts, "."),
		ArtifactID: artifactID,
		Version:    version,
		File:       file,
	}, nil
}
