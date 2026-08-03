// Package enrich provides server-side User-Agent, GeoIP, and URL/UTM decomposition.
package enrich

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"

	"github.com/dgraph-io/ristretto"
	geoip2 "github.com/oschwald/geoip2-golang/v2"
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
	geoDB    *geoip2.Reader // nil when GEOIP_DB_PATH is unset (GeoIP disabled)
}

// NewEnricher compiles User-Agent regex rules once at startup and sets up caching.
//
// geoDBPath is an optional path to a MaxMind GeoLite2-City.mmdb file. If empty,
// GeoIP lookups are skipped and Country/Region/City stay blank -- this is a
// valid, deliberate configuration, not an error.
func NewEnricher(geoDBPath string) (*Enricher, error) {
	parser := uaparser.NewFromSaved()

	cache, err := ristretto.NewCache(&ristretto.Config{
		NumCounters: 1e5,     // 100k entry counter
		MaxCost:     1 << 20, // 1MB max cache budget
		BufferItems: 64,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create UA cache: %w", err)
	}

	var geoDB *geoip2.Reader
	if geoDBPath != "" {
		geoDB, err = geoip2.Open(geoDBPath)
		if err != nil {
			return nil, fmt.Errorf("failed to open GeoIP database at %q: %w", geoDBPath, err)
		}
	}

	return &Enricher{
		uaParser: parser,
		uaCache:  cache,
		geoDB:    geoDB,
	}, nil
}

// Close releases the GeoIP database file handle, if one was opened.
func (e *Enricher) Close() error {
	if e.geoDB != nil {
		return e.geoDB.Close()
	}
	return nil
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

	// 4. GeoIP lookup. Runs regardless of storeIP: country/city are derived,
	// aggregate-level data, distinct from retaining the raw IP itself (§6).
	if rawIP != "" {
		if parsedIP := net.ParseIP(rawIP); parsedIP != nil {
			e.lookupGeo(parsedIP, res)

			// IP Privacy & Retention Rule (§6): only persist the raw IP if the
			// tenant has opted in.
			if storeIP {
				res.ParsedIP = parsedIP
			}
		}
	}

	return res
}

// lookupGeo resolves country/region/city from the GeoIP database, if configured.
func (e *Enricher) lookupGeo(parsedIP net.IP, res *EnrichedContext) {
	if e.geoDB == nil {
		return
	}

	addr, ok := netip.AddrFromSlice(parsedIP.To16())
	if !ok {
		return
	}

	record, err := e.geoDB.City(addr)
	if err != nil {
		return
	}

	res.Country = record.Country.ISOCode
	res.City = record.City.Names.English
	if len(record.Subdivisions) > 0 {
		res.Region = record.Subdivisions[0].Names.English
	}
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
