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

func TokenAuthHeader(token string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte("token:" + token))
	return "Basic " + encoded
}

func IsNavPackage(groupID string) bool {
	return strings.HasPrefix(groupID, "com.github.navikt") ||
		strings.HasPrefix(groupID, "no.nav") ||
		strings.HasPrefix(groupID, "no.stelvio")
}

func IsMavenMetadataXml(path string) bool {
	return strings.HasSuffix(path, "/maven-metadata.xml") || path == "maven-metadata.xml"
}

func IsValidMavenCoordinate(s string) bool {
	return validMavenCoordinate.MatchString(s)
}

func IsValidPathSegment(s string) bool {
	return validPathSegment.MatchString(s)
}

func ModifiedHeadersWithAuth(token string) http.Header {
	h := http.Header{}
	h.Set("Authorization", TokenAuthHeader(token))
	return h
}

func ParsePathAsArtifact(path string) (Artifact, error) {
	segments := strings.Split(path, "/")

	if strings.HasSuffix(path, "maven-metadata.xml") {
		if len(segments) < 2 {
			return Artifact{}, fmt.Errorf("not a valid Maven repository path")
		}
		return Artifact{
			GroupID:    strings.Join(segments[:len(segments)-2], "."),
			ArtifactID: segments[len(segments)-2],
			File:       segments[len(segments)-1],
		}, nil
	}

	if len(segments) < 4 {
		return Artifact{}, fmt.Errorf("not a valid Maven repository path")
	}

	return Artifact{
		GroupID:    strings.Join(segments[:len(segments)-3], "."),
		ArtifactID: segments[len(segments)-3],
		Version:    segments[len(segments)-2],
		File:       segments[len(segments)-1],
	}, nil
}
