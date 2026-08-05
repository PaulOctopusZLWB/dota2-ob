package preflight

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var uriPattern = regexp.MustCompile(`(?i)"uri"\s*"([^"]+)"`)

func NormalizeListenAddress(address string) (string, error) {
	host, portText, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return "", errors.New("listen_address_invalid")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", errors.New("listen_address_invalid")
	}
	switch host {
	case "localhost":
		host = "127.0.0.1"
	case "127.0.0.1", "::1":
	default:
		return "", errors.New("listen_address_not_loopback")
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

func ValidateGSIConfig(data []byte, normalizedAddress string) error {
	match := uriPattern.FindSubmatch(data)
	if len(match) != 2 {
		return errors.New("gsi_config_uri_missing")
	}
	u, err := url.Parse(string(match[1]))
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/gsi" {
		return errors.New("gsi_config_uri_invalid")
	}
	endpoint, err := NormalizeListenAddress(u.Host)
	if err != nil || endpoint != normalizedAddress {
		return fmt.Errorf("gsi_config_uri_mismatch")
	}
	return nil
}
