package main

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

func TestStorage(t *testing.T) {
	ctx := context.Background()

	t.Run("write then read round-trip", func(t *testing.T) {
		dir := t.TempDir()
		s := NewLocalStorage(dir)
		w := s.File("artifact.jar").NewWriter(ctx)
		if _, err := io.Copy(w, strings.NewReader("hello world")); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		r, err := s.File("artifact.jar").NewReader(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		data, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "hello world" {
			t.Errorf("got %q, want %q", data, "hello world")
		}
	})

	t.Run("Exists returns true for existing file", func(t *testing.T) {
		dir := t.TempDir()
		s := NewLocalStorage(dir)
		w := s.File("present.txt").NewWriter(ctx)
		_, _ = w.Write([]byte("x"))
		_ = w.Close()
		ok, err := s.File("present.txt").Exists(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Error("expected Exists=true for written file")
		}
	})

	t.Run("Exists returns false for missing file", func(t *testing.T) {
		dir := t.TempDir()
		s := NewLocalStorage(dir)
		ok, err := s.File("nonexistent.txt").Exists(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Error("expected Exists=false for missing file")
		}
	})

	t.Run("GetMetadata returns valid TimeCreated", func(t *testing.T) {
		dir := t.TempDir()
		s := NewLocalStorage(dir)
		before := time.Now().Add(-time.Second)
		w := s.File("meta.txt").NewWriter(ctx)
		_, _ = w.Write([]byte("x"))
		_ = w.Close()
		meta, err := s.File("meta.txt").GetMetadata(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if meta.TimeCreated.IsZero() {
			t.Error("TimeCreated is zero")
		}
		if meta.TimeCreated.Before(before) {
			t.Errorf("TimeCreated %v is before test start %v", meta.TimeCreated, before)
		}
	})

	t.Run("Delete removes file", func(t *testing.T) {
		dir := t.TempDir()
		s := NewLocalStorage(dir)
		w := s.File("delete-me.txt").NewWriter(ctx)
		_, _ = w.Write([]byte("x"))
		_ = w.Close()
		if err := s.File("delete-me.txt").Delete(ctx); err != nil {
			t.Fatal(err)
		}
		ok, err := s.File("delete-me.txt").Exists(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Error("expected Exists=false after delete")
		}
	})

	t.Run("NewWriter creates nested directories", func(t *testing.T) {
		dir := t.TempDir()
		s := NewLocalStorage(dir)
		w := s.File("deep/nested/path/file.txt").NewWriter(ctx)
		_, _ = w.Write([]byte("nested"))
		_ = w.Close()
		ok, err := s.File("deep/nested/path/file.txt").Exists(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Error("expected Exists=true for nested file")
		}
	})

	t.Run("NewReader on missing file returns error", func(t *testing.T) {
		dir := t.TempDir()
		s := NewLocalStorage(dir)
		_, err := s.File("missing.txt").NewReader(ctx)
		if err == nil {
			t.Error("expected error for missing file, got nil")
		}
	})
}
