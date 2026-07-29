// Package enrich provides server-side User-Agent, GeoIP, and URL/UTM decomposition.
package enrich

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/dgraph-io/ristretto"
	uaparser "github.com/ua-parser/uap-go/uaparser"
)

// EnrichedContext holds derived server-side attributes for an event context.
type EnrichedContext struct {
	Browser        string
	BrowserVersion string
	OS             string
	OSVersion      string
	DeviceType     string // desktop | mobile | tablet | server | bot
	Manufacturer   string
	Path           string
	Host           string
	ReferrerHost   string
	UTMSource      string
	UTMMedium      string
	UTMCampaign    string
	UTMTerm        string
	UTMContent     string
	Country        string
	Region         string
	City           string
	ParsedIP       net.IP
}

// CachedUA stores parsed UA results in the Ristretto LRU cache.
type CachedUA struct {
	Browser        string
	BrowserVersion string
	OS             string
	OSVersion      string
	DeviceType     string
	Manufacturer   string
}

// Enricher coordinates high-speed server-side enrichment with caching.
type Enricher struct {
	uaParser *uaparser.Parser
	uaCache  *ristretto.Cache
}

// NewEnricher compiles User-Agent regex rules once at startup and sets up caching.
func NewEnricher() (*Enricher, error) {
	parser := uaparser.NewFromSaved()

	cache, err := ristretto.NewCache(&ristretto.Config{
		NumCounters: 1e5,     // 100k entry counter
		MaxCost:     1 << 20, // 1MB max cache budget
		BufferItems: 64,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create UA cache: %w", err)
	}

	return &Enricher{
		uaParser: parser,
		uaCache:  cache,
	}, nil
}

// Enrich parses raw inputs (UA, URL, referrer, IP) into structured, typed attributes.
func (e *Enricher) Enrich(rawUA, rawIP, rawURL, rawReferrer string, storeIP bool) *EnrichedContext {
	res := &EnrichedContext{}

	// 1. Parse User-Agent (cached via Ristretto)
	if rawUA != "" {
		if cached, found := e.uaCache.Get(rawUA); found {
			if cua, ok := cached.(CachedUA); ok {
				res.Browser = cua.Browser
				res.BrowserVersion = cua.BrowserVersion
				res.OS = cua.OS
				res.OSVersion = cua.OSVersion
				res.DeviceType = cua.DeviceType
				res.Manufacturer = cua.Manufacturer
			}
		} else {
			client := e.uaParser.Parse(rawUA)
			cua := CachedUA{
				Browser:        client.UserAgent.Family,
				BrowserVersion: strings.TrimSpace(client.UserAgent.Major + "." + client.UserAgent.Minor),
				OS:             client.Os.Family,
				OSVersion:      strings.TrimSpace(client.Os.Major + "." + client.Os.Minor),
				DeviceType:     classifyDevice(client),
				Manufacturer:   client.Device.Brand,
			}

			e.uaCache.Set(rawUA, cua, 1)
			res.Browser = cua.Browser
			res.BrowserVersion = cua.BrowserVersion
			res.OS = cua.OS
			res.OSVersion = cua.OSVersion
			res.DeviceType = cua.DeviceType
			res.Manufacturer = cua.Manufacturer
		}
	}

	// 2. Parse URL and UTM Parameters
	if rawURL != "" {
		if parsedURL, err := url.Parse(rawURL); err == nil {
			res.Host = parsedURL.Host
			res.Path = parsedURL.Path

			query := parsedURL.Query()
			res.UTMSource = query.Get("utm_source")
			res.UTMMedium = query.Get("utm_medium")
			res.UTMCampaign = query.Get("utm_campaign")
			res.UTMTerm = query.Get("utm_term")
			res.UTMContent = query.Get("utm_content")
		}
	}

	// 3. Parse Referrer Host
	if rawReferrer != "" {
		if parsedRef, err := url.Parse(rawReferrer); err == nil {
			res.ReferrerHost = parsedRef.Host
		}
	}

	// 4. IP Privacy & Retention Rule (§6)
	if storeIP && rawIP != "" {
		res.ParsedIP = net.ParseIP(rawIP)
	}

	return res
}

// Classify device type based on parsed User-Agent metadata.
func classifyDevice(client *uaparser.Client) string {
	family := strings.ToLower(client.Device.Family)
	uaFamily := strings.ToLower(client.UserAgent.Family)
	osFamily := strings.ToLower(client.Os.Family)

	if strings.Contains(family, "bot") || strings.Contains(uaFamily, "bot") || strings.Contains(uaFamily, "spider") || strings.Contains(uaFamily, "crawler") {
		return "bot"
	}
	if strings.Contains(family, "ipad") || strings.Contains(family, "tablet") || strings.Contains(family, "kindle") {
		return "tablet"
	}
	if strings.Contains(family, "iphone") || strings.Contains(family, "mobile") || strings.Contains(family, "phone") {
		return "mobile"
	}
	if strings.Contains(osFamily, "mac") || strings.Contains(osFamily, "windows") || strings.Contains(osFamily, "linux") || strings.Contains(osFamily, "ubuntu") || strings.Contains(osFamily, "chrome os") {
		return "desktop"
	}
	if client.Device.Family != "Other" && client.Device.Family != "" {
		return "mobile"
	}
	return "desktop"
}
