package main

import (
	"testing"
)

func TestResolveAPIURLLocksHost(t *testing.T) {
	c, err := newAPIClient("http://localhost:8080", "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.resolveAPIURL("/api/v1/reports")
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://localhost:8080/api/v1/reports" {
		t.Fatalf("got %q", got)
	}
	if _, err := c.resolveAPIURL("https://evil.example/steal"); err == nil {
		t.Fatal("expected absolute URL rejection")
	}
	if _, err := c.resolveAPIURL("//evil.example/steal"); err == nil {
		t.Fatal("expected protocol-relative rejection")
	}
}

func TestNewAPIClientRejectsBadScheme(t *testing.T) {
	if _, err := newAPIClient("ftp://localhost:8080", ""); err == nil {
		t.Fatal("expected scheme rejection")
	}
}
