package main

import (
	"os"
	"testing"
)

func TestPluginLogoConfiguration(t *testing.T) {
	registration := pluginRegistration()
	if registration.Metadata.Logo != pluginLogo {
		t.Fatalf("registration logo = %q, want %q", registration.Metadata.Logo, pluginLogo)
	}
	if info, errStat := os.Stat("assets/logo.png"); errStat != nil {
		t.Fatalf("logo asset is missing: %v", errStat)
	} else if info.Size() == 0 {
		t.Fatal("logo asset is empty")
	}
}
