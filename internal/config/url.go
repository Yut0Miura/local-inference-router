package config

import (
	"errors"
	"net/url"
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
	case parsed.Host == "":
		return nil, errors.New("must contain a host")
	case parsed.User != nil:
		return nil, errors.New("must not contain credentials")
	case parsed.RawQuery != "" || parsed.ForceQuery:
		return nil, errors.New("must not contain a query")
	case parsed.Fragment != "":
		return nil, errors.New("must not contain a fragment")
	case parsed.Path != "" && parsed.Path != "/":
		return nil, errors.New("must not contain a path")
	case parsed.Opaque != "":
		return nil, errors.New("must be an absolute origin URL")
	}

	return &url.URL{Scheme: parsed.Scheme, Host: parsed.Host}, nil
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
