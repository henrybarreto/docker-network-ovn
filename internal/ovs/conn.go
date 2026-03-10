package ovs

import "strings"

// NormalizeConn ensures the connection string has a proper scheme
func NormalizeConn(conn string) string {
	if strings.HasPrefix(conn, "unix:") || strings.HasPrefix(conn, "tcp:") ||
		strings.HasPrefix(conn, "ssl:") || strings.HasPrefix(conn, "ptcp:") ||
		strings.HasPrefix(conn, "pssl:") {
		return conn
	}

	if strings.HasPrefix(conn, "/") {
		return "unix:" + conn
	}

	if strings.Contains(conn, ":") {
		return "tcp:" + conn
	}

	return "unix:" + conn
}
