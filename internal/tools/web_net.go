package tools

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
	"unicode"
)

const (
	webDirectTimeout          = 15 * time.Second
	webFirecrawlScrapeTimeout = 25 * time.Second
	maxFetchRedirects         = 3
	maxFetchURLLen            = 2048
	maxResponseHeadersBytes   = 64 << 10  // 64 KiB
	maxCompressedBodyBytes    = 512 << 10 // 512 KiB pre-decompression read limit
	maxDecompressedBodyBytes  = 512 << 10 // 512 KiB post-decompression read limit
	maxSearchResponseBytes    = 256 << 10 // 256 KiB JSON response limit for search providers
	maxErrorBodyBytes         = 4096
)

var (
	errSearchRedirectBlocked = errors.New("redirects are disabled for search provider requests")
)

type ipResolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

type webDialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// webNetHooks is an internal test seam used strictly in unit tests to inject
// mock DNS resolvers, dialer interceptors, and test root CAs without enabling
// local/loopback SSRF bypasses in production. Production code leaves it nil.
type webNetHooks struct {
	resolver ipResolver
	rawDial  webDialFunc
	rootCAs  *x509.CertPool
}

var blockedIPPrefixes = []netip.Prefix{
	// IPv4 prohibited ranges
	netip.MustParsePrefix("0.0.0.0/8"),       // "This" network / unspecified
	netip.MustParsePrefix("10.0.0.0/8"),      // RFC 1918 private
	netip.MustParsePrefix("100.64.0.0/10"),   // RFC 6598 Shared Address Space (CGNAT)
	netip.MustParsePrefix("127.0.0.0/8"),     // Loopback
	netip.MustParsePrefix("169.254.0.0/16"),  // Link-local / Cloud metadata
	netip.MustParsePrefix("172.16.0.0/12"),   // RFC 1918 private
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // TEST-NET-1
	netip.MustParsePrefix("192.88.99.0/24"),  // Deprecated 6to4 relay
	netip.MustParsePrefix("192.168.0.0/16"),  // RFC 1918 private
	netip.MustParsePrefix("198.18.0.0/15"),   // Benchmarking
	netip.MustParsePrefix("198.51.100.0/24"), // TEST-NET-2
	netip.MustParsePrefix("203.0.113.0/24"),  // TEST-NET-3
	netip.MustParsePrefix("224.0.0.0/4"),     // Multicast
	netip.MustParsePrefix("240.0.0.0/4"),     // Reserved & Broadcast (255.255.255.255/32)

	// IPv6 prohibited ranges
	netip.MustParsePrefix("::/128"),          // Unspecified
	netip.MustParsePrefix("::1/128"),         // Loopback
	netip.MustParsePrefix("::/96"),           // Deprecated IPv4-compatible IPv6
	netip.MustParsePrefix("::ffff:0:0/96"),   // IPv4-mapped IPv6
	netip.MustParsePrefix("::ffff:0:0:0/96"), // RFC 2765 IPv4-translated
	netip.MustParsePrefix("64:ff9b::/96"),    // NAT64 Well-Known Prefix
	netip.MustParsePrefix("64:ff9b:1::/48"),  // NAT64 Local-Use Prefix
	netip.MustParsePrefix("100::/64"),        // Discard-Only
	netip.MustParsePrefix("2001::/32"),       // Teredo
	netip.MustParsePrefix("2001:2::/48"),     // Benchmarking
	netip.MustParsePrefix("2001:10::/28"),    // ORCHID (deprecated)
	netip.MustParsePrefix("2001:20::/28"),    // ORCHIDv2
	netip.MustParsePrefix("2001:db8::/32"),   // Documentation
	netip.MustParsePrefix("2002::/16"),       // 6to4 (can embed private IPv4)
	netip.MustParsePrefix("fc00::/7"),        // Unique Local Address (private)
	netip.MustParsePrefix("fe80::/10"),       // Link-local
	netip.MustParsePrefix("fec0::/10"),       // Deprecated site-local
	netip.MustParsePrefix("ff00::/8"),        // Multicast
}

// validatePublicIP verifies that an IP address is a strictly public routable unicast IP.
// It unmaps IPv4-mapped IPv6 addresses and checks both stdlib properties and explicit CIDR lists.
func validatePublicIP(ipAddr net.IPAddr) (netip.Addr, error) {
	if ipAddr.Zone != "" {
		return netip.Addr{}, errors.New("IP zone identifiers are not allowed")
	}
	addr, ok := netip.AddrFromSlice(ipAddr.IP)
	if !ok || !addr.IsValid() {
		return netip.Addr{}, errors.New("invalid IP address")
	}
	addr = addr.Unmap()

	if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() || addr.IsInterfaceLocalMulticast() ||
		addr.IsMulticast() || addr.IsUnspecified() || !addr.IsGlobalUnicast() {
		return netip.Addr{}, fmt.Errorf("non-public IP address prohibited: %s", addr.String())
	}

	for _, prefix := range blockedIPPrefixes {
		if prefix.Contains(addr) {
			return netip.Addr{}, fmt.Errorf("IP address %s falls within prohibited range %s", addr.String(), prefix.String())
		}
	}
	return addr, nil
}

// isNonCanonicalNumericHost rejects decimal integer hosts (e.g. 2130706433),
// octal-dotted forms (e.g. 0177.0.0.1), hex forms (0x7f000001), and shorthand IPs (127.1).
func isNonCanonicalNumericHost(host string) bool {
	if host == "" {
		return false
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return false
	}
	trimmed := strings.TrimSuffix(host, ".")
	parts := strings.Split(trimmed, ".")
	if len(parts) == 0 {
		return false
	}
	allNumericLike := true
	for _, part := range parts {
		if part == "" {
			continue
		}
		lower := strings.ToLower(part)
		if strings.HasPrefix(lower, "0x") {
			if len(lower) > 2 && isHexDigits(lower[2:]) {
				continue
			}
			allNumericLike = false
			break
		}
		if isASCIIAllDigits(lower) {
			continue
		}
		allNumericLike = false
		break
	}
	return allNumericLike
}

func isASCIIAllDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}

func isHexDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return len(s) > 0
}

func isBlockedHostname(host string) bool {
	h := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if h == "" {
		return true
	}
	if strings.ContainsAny(h, " \t\r\n\\@/%") {
		return true
	}
	for _, r := range h {
		if r < 0x20 || unicode.IsSpace(r) || unicode.IsControl(r) {
			return true
		}
	}
	if h == "localhost" || strings.HasSuffix(h, ".localhost") ||
		strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".internal") ||
		strings.HasSuffix(h, ".localdomain") || strings.HasSuffix(h, ".arpa") {
		return true
	}
	// Require an FQDN with at least one dot for non-IP hostnames to block
	// internal single-label DNS search-suffix hosts like "metadata" or "redis".
	if !strings.Contains(h, ".") {
		return true
	}
	return false
}

// validateExternalURL enforces strict HTTPS-only public URL rules without userinfo.
func validateExternalURL(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, errors.New("URL is required")
	}
	if len(raw) > maxFetchURLLen {
		return nil, fmt.Errorf("URL length %d exceeds %d-byte limit", len(raw), maxFetchURLLen)
	}
	if strings.ContainsAny(raw, "\x00\r\n\t \\") {
		return nil, errors.New("URL contains prohibited whitespace or control characters")
	}

	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("malformed URL: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return nil, fmt.Errorf("unsupported URL scheme %q: only https:// is allowed", u.Scheme)
	}
	u.Scheme = "https"

	if u.User != nil {
		return nil, errors.New("userinfo (user:pass@host) in URL is prohibited")
	}
	if u.Opaque != "" {
		return nil, errors.New("opaque URLs are not allowed")
	}

	host := strings.ToLower(u.Hostname())
	if host == "" {
		return nil, errors.New("URL must specify a valid host")
	}

	if ipAddr, parseErr := netip.ParseAddr(host); parseErr == nil {
		if _, err := validatePublicIP(net.IPAddr{IP: ipAddr.AsSlice(), Zone: ipAddr.Zone()}); err != nil {
			return nil, err
		}
	} else {
		if isNonCanonicalNumericHost(host) {
			return nil, fmt.Errorf("non-canonical numeric host %q is prohibited", host)
		}
		if isBlockedHostname(host) {
			return nil, fmt.Errorf("host %q is blocked by SSRF policy", host)
		}
	}

	if p := u.Port(); p != "" {
		if !isASCIIAllDigits(p) {
			return nil, fmt.Errorf("invalid port %q", p)
		}
		var portNum int
		for i := 0; i < len(p); i++ {
			portNum = portNum*10 + int(p[i]-'0')
			if portNum > 65535 {
				return nil, fmt.Errorf("port out of range: %s", p)
			}
		}
		if portNum < 1 {
			return nil, fmt.Errorf("port out of range: %s", p)
		}
		u.Host = net.JoinHostPort(host, p)
	} else if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	} else {
		u.Host = host
	}

	return u, nil
}

// resolveAndVerifyPublicIPs resolves host and guarantees ALL returned IPs pass public IP checks.
// Mixed public+private DNS results are rejected conservatively.
func resolveAndVerifyPublicIPs(ctx context.Context, host string, resolver ipResolver) ([]netip.Addr, error) {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	if ipAddr, err := netip.ParseAddr(host); err == nil {
		verified, vErr := validatePublicIP(net.IPAddr{IP: ipAddr.AsSlice(), Zone: ipAddr.Zone()})
		if vErr != nil {
			return nil, vErr
		}
		return []netip.Addr{verified}, nil
	}

	if isNonCanonicalNumericHost(host) || isBlockedHostname(host) {
		return nil, fmt.Errorf("host %q is blocked by SSRF policy", host)
	}

	ips, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("DNS lookup failed for %q: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("DNS lookup returned no addresses for %q", host)
	}

	verified := make([]netip.Addr, 0, len(ips))
	for _, candidate := range ips {
		addr, err := validatePublicIP(candidate)
		if err != nil {
			return nil, fmt.Errorf("DNS resolution for %q returned prohibited address: %w", host, err)
		}
		verified = append(verified, addr)
	}
	return verified, nil
}

func safeDialContext(ctx context.Context, network, addr string, resolver ipResolver, rawDial webDialFunc) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("invalid dial address: %w", err)
	}
	verifiedIPs, err := resolveAndVerifyPublicIPs(ctx, host, resolver)
	if err != nil {
		return nil, fmt.Errorf("outbound connection blocked: %w", err)
	}

	var lastErr error
	for _, targetIP := range verifiedIPs {
		targetAddr := net.JoinHostPort(targetIP.String(), port)
		conn, dialErr := rawDial(ctx, network, targetAddr)
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	return nil, lastErr
}

func buildHardenedTransport(hooks *webNetHooks, timeout time.Duration) *http.Transport {
	if timeout <= 0 {
		timeout = webDirectTimeout
	}
	dialTimeout := min(10*time.Second, timeout)
	respHdrTimeout := timeout - 3*time.Second
	if respHdrTimeout < 1*time.Second {
		respHdrTimeout = timeout
	}

	var resolver ipResolver = net.DefaultResolver
	var rawDial webDialFunc = (&net.Dialer{Timeout: dialTimeout}).DialContext
	var rootCAs *x509.CertPool
	if hooks != nil {
		if hooks.resolver != nil {
			resolver = hooks.resolver
		}
		if hooks.rawDial != nil {
			rawDial = hooks.rawDial
		}
		if hooks.rootCAs != nil {
			rootCAs = hooks.rootCAs
		}
	}

	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}
	if rootCAs != nil {
		tlsCfg.RootCAs = rootCAs
	}

	return &http.Transport{
		Proxy:                  nil,
		DisableKeepAlives:      true,
		DisableCompression:     true,
		MaxIdleConns:           0,
		MaxConnsPerHost:        2,
		MaxResponseHeaderBytes: maxResponseHeadersBytes,
		TLSHandshakeTimeout:    dialTimeout,
		ResponseHeaderTimeout:  respHdrTimeout,
		TLSClientConfig:        tlsCfg,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return safeDialContext(ctx, network, addr, resolver, rawDial)
		},
	}
}

func createSearchHTTPClient(hooks *webNetHooks, timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = webDirectTimeout
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: buildHardenedTransport(hooks, timeout),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return errSearchRedirectBlocked
		},
	}
}

func createFetchHTTPClient(hooks *webNetHooks, timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = webDirectTimeout
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: buildHardenedTransport(hooks, timeout),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxFetchRedirects {
				return fmt.Errorf("stopped after %d redirects", maxFetchRedirects)
			}
			if _, err := validateExternalURL(req.URL.String()); err != nil {
				return fmt.Errorf("redirect blocked: %w", err)
			}
			// Prevent credential and referrer leakage across any redirect hops
			req.Header.Del("Authorization")
			req.Header.Del("Cookie")
			req.Header.Del("Proxy-Authorization")
			req.Header.Del("X-Subscription-Token")
			req.Header.Del("X-Api-Key")
			req.Header.Del("Referer")
			return nil
		},
	}
}

// readBoundedBody reads compressed stream up to compLimit and decompressed stream up to decompLimit.
func readBoundedBody(resp *http.Response, compLimit, decompLimit int64) ([]byte, bool, error) {
	compData, err := io.ReadAll(io.LimitReader(resp.Body, compLimit+1))
	if err != nil {
		return nil, false, fmt.Errorf("reading response body: %w", err)
	}
	if int64(len(compData)) > compLimit {
		if enc := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding"))); enc == "" || enc == "identity" {
			return compData[:compLimit], true, nil
		}
		return nil, true, fmt.Errorf("compressed response exceeds %d-byte limit", compLimit)
	}

	enc := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))
	var reader io.Reader
	var closer io.Closer
	switch enc {
	case "", "identity":
		return compData, false, nil
	case "gzip":
		gz, err := gzip.NewReader(bytes.NewReader(compData))
		if err != nil {
			return nil, false, fmt.Errorf("invalid gzip body: %w", err)
		}
		closer = gz
		reader = gz
	case "deflate":
		fl := flate.NewReader(bytes.NewReader(compData))
		closer = fl
		reader = fl
	default:
		return nil, false, fmt.Errorf("unsupported Content-Encoding %q", enc)
	}
	if closer != nil {
		defer closer.Close()
	}

	decomp, err := io.ReadAll(io.LimitReader(reader, decompLimit+1))
	if err != nil {
		return nil, false, fmt.Errorf("decompressing response body: %w", err)
	}
	if int64(len(decomp)) > decompLimit {
		return decomp[:decompLimit], true, nil
	}
	return decomp, false, nil
}

// redactURLWithoutQuery formats URL strictly as scheme://host/path (omitting userinfo & query string).
func redactURLWithoutQuery(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Hostname() == "" {
		return "<redacted-url>"
	}
	host := u.Hostname()
	if port := u.Port(); port != "" {
		host = net.JoinHostPort(host, port)
	}
	p := u.EscapedPath()
	return u.Scheme + "://" + host + p
}

// sanitizeNetworkError strips query strings, userinfo, and secret tokens from errors.
func sanitizeNetworkError(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		safeErr := *urlErr
		safeErr.URL = redactURLWithoutQuery(urlErr.URL)
		safeErr.Err = sanitizeNetworkError(urlErr.Err, secrets...)
		msg := redactSecrets(safeErr.Error(), secrets...)
		return errors.New(msg)
	}
	return errors.New(redactSecrets(err.Error(), secrets...))
}

// sanitizeSearchError strips URL query strings, secret keys, and sensitive
// queries from search errors without corrupting messages on short queries.
func sanitizeSearchError(err error, secretKey, sensitiveQuery string) error {
	if err == nil {
		return nil
	}
	clean := sanitizeNetworkError(err, secretKey)
	return errors.New(redactSensitiveQuery(clean.Error(), sensitiveQuery))
}

func redactSensitiveQuery(msg, query string) string {
	if query == "" {
		return msg
	}
	escaped := url.QueryEscape(query)
	pathEsc := url.PathEscape(query)
	if len(query) >= 6 {
		msg = strings.ReplaceAll(msg, query, "[REDACTED]")
		msg = strings.ReplaceAll(msg, escaped, "[REDACTED]")
		msg = strings.ReplaceAll(msg, pathEsc, "[REDACTED]")
		return msg
	}
	// For short queries (< 6 bytes), avoid blind substring replacement that would
	// corrupt normal words (e.g., query="go" breaking "google" or "got").
	for _, key := range []string{"q=", "query="} {
		for _, val := range []string{escaped, pathEsc, query} {
			if val != "" {
				msg = strings.ReplaceAll(msg, key+val, key+"[REDACTED]")
			}
		}
	}
	return msg
}

func redactSecrets(msg string, secrets ...string) string {
	for _, s := range secrets {
		if s == "" {
			continue
		}
		msg = strings.ReplaceAll(msg, s, "[REDACTED]")
		msg = strings.ReplaceAll(msg, url.QueryEscape(s), "[REDACTED]")
		msg = strings.ReplaceAll(msg, url.PathEscape(s), "[REDACTED]")
	}
	return msg
}
