package douyinim

// Netscape cookies.txt parser → Cookie header for douyin.com requests.

import (
	"os"
	"strings"
)

// Cookie is a single parsed cookie.
type Cookie struct {
	Name   string
	Value  string
	Domain string
	Path   string
	Secure bool
}

// LoadCookies parses a Netscape-format cookies.txt file.
func LoadCookies(path string) ([]Cookie, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseCookies(string(data)), nil
}

// ParseCookies parses Netscape cookies.txt content.
func ParseCookies(text string) []Cookie {
	var cookies []Cookie
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		// Keep #HttpOnly_ lines (strip the marker); skip other comments.
		if strings.HasPrefix(line, "#") {
			if !strings.HasPrefix(line, "#HttpOnly_") {
				continue
			}
			line = line[len("#HttpOnly_"):]
		}
		p := strings.Split(line, "\t")
		if len(p) < 7 {
			continue
		}
		cookies = append(cookies, Cookie{
			Domain: p[0],
			Path:   p[2],
			Secure: strings.EqualFold(p[3], "TRUE"),
			Name:   p[5],
			Value:  p[6],
		})
	}
	return cookies
}

func domainMatch(cookieDomain, host string) bool {
	d := strings.TrimPrefix(cookieDomain, ".")
	return host == d || strings.HasSuffix(host, "."+d)
}

// cookieHeader builds a "k=v; k=v" Cookie header for the given host.
func cookieHeader(cookies []Cookie, host string) string {
	var parts []string
	for _, c := range cookies {
		if domainMatch(c.Domain, host) {
			parts = append(parts, c.Name+"="+c.Value)
		}
	}
	return strings.Join(parts, "; ")
}

// getCookie looks up a single cookie value (first match by name for host).
func getCookie(cookies []Cookie, host, name string) string {
	for _, c := range cookies {
		if c.Name == name && domainMatch(c.Domain, host) {
			return c.Value
		}
	}
	return ""
}
