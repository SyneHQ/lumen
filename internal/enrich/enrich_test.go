package enrich

import (
	"testing"
)

func TestEnricherURLAndUTM(t *testing.T) {
	enricher, err := NewEnricher("")
	if err != nil {
		t.Fatalf("Failed to create enricher: %v", err)
	}

	rawURL := "https://example.com/checkout?utm_source=google&utm_medium=cpc&utm_campaign=summer_sale"
	rawReferrer := "https://google.com/search"
	rawUA := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

	res := enricher.Enrich(rawUA, "1.2.3.4", rawURL, rawReferrer, false)

	if res.Host != "example.com" {
		t.Errorf("Expected Host 'example.com', got '%s'", res.Host)
	}

	if res.Path != "/checkout" {
		t.Errorf("Expected Path '/checkout', got '%s'", res.Path)
	}

	if res.UTMSource != "google" {
		t.Errorf("Expected UTMSource 'google', got '%s'", res.UTMSource)
	}

	if res.UTMMedium != "cpc" {
		t.Errorf("Expected UTMMedium 'cpc', got '%s'", res.UTMMedium)
	}

	if res.UTMCampaign != "summer_sale" {
		t.Errorf("Expected UTMCampaign 'summer_sale', got '%s'", res.UTMCampaign)
	}

	if res.ReferrerHost != "google.com" {
		t.Errorf("Expected ReferrerHost 'google.com', got '%s'", res.ReferrerHost)
	}

	if res.Browser != "Chrome" {
		t.Errorf("Expected Browser 'Chrome', got '%s'", res.Browser)
	}

	if res.OS != "Mac OS X" {
		t.Errorf("Expected OS 'Mac OS X', got '%s'", res.OS)
	}

	if res.DeviceType != "desktop" {
		t.Errorf("Expected DeviceType 'desktop', got '%s'", res.DeviceType)
	}

	// Verify privacy rule: IP should not be stored when storeIP is false
	if res.ParsedIP != nil {
		t.Errorf("Expected ParsedIP to be nil when storeIP is false")
	}
}
