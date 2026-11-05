package config

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// ChatCompletionsPath is the only inference endpoint the router calls upstream.
const ChatCompletionsPath = "/v1/chat/completions"

// ParseBaseURL validates that a backend base URL is a bare origin: an http or
// https scheme, a host, and nothing else. A path, query, fragment or userinfo
// is rejected so that upstream URLs cannot smuggle in extra routing.
func ParseBaseURL(raw string) (*url.URL, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, errors.New("must not be empty")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return nil, errors.New("is not a valid URL")
	}
	switch {
	case parsed.Scheme != "http" && parsed.Scheme != "https":
		return nil, errors.New("must use http or https")
	case parsed.Hostname() == "":
		return nil, errors.New("must contain a host")
	case parsed.User != nil:
		return nil, errors.New("must not contain credentials")
	case strings.HasSuffix(parsed.Host, ":"):
		return nil, errors.New("must contain a valid port")
	case strings.Count(parsed.Host, ":") > 1 && !strings.HasPrefix(parsed.Host, "["):
		return nil, errors.New("IPv6 host must use brackets")
	case parsed.RawQuery != "" || parsed.ForceQuery:
		return nil, errors.New("must not contain a query")
	case parsed.Fragment != "":
		return nil, errors.New("must not contain a fragment")
	case parsed.Path != "" && parsed.Path != "/":
		return nil, errors.New("must not contain a path")
	case parsed.Opaque != "":
		return nil, errors.New("must be an absolute origin URL")
	}

	if port := parsed.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return nil, errors.New("port must be between 1 and 65535")
		}
	}

	return &url.URL{Scheme: parsed.Scheme, Host: parsed.Host}, nil
}

// canonicalOriginKey returns one stable key for a validated backend origin.
// Host names are case-insensitive, IP literals are normalized and an omitted
// default port is made explicit.
func canonicalOriginKey(base *url.URL) string {
	scheme := strings.ToLower(base.Scheme)

	host := strings.ToLower(base.Hostname())
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	}

	port := base.Port()
	if port == "" {
		switch scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		}
	} else if number, err := strconv.Atoi(port); err == nil {
		// ParseBaseURL has already validated the port. Re-encoding it removes
		// harmless formatting differences such as leading zeroes.
		port = strconv.Itoa(number)
	}

	return scheme + "://" + net.JoinHostPort(host, port)
}

// EndpointURL returns the base URL with a fixed path. URLs are built through
// url.URL, never by string concatenation, and the path is never user input.
func EndpointURL(base *url.URL, path string) *url.URL {
	endpoint := *base
	endpoint.Path = path
	endpoint.RawQuery = ""
	endpoint.Fragment = ""
	endpoint.User = nil
	return &endpoint
}
