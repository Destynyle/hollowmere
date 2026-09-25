package session

import (
	"fmt"
	"io"
	"sync/atomic"
	"time"
)

// Metrics are the counters exported on /metrics in Prometheus text format.
// Counters only go up; gauges are read from the live state when rendering.
type Metrics struct {
	start time.Time

	ConnectionsTotal      atomic.Int64
	ConnectionsOpen       atomic.Int64
	ConnectionsRejected   atomic.Int64
	Commands              atomic.Int64
	Errors                atomic.Int64
	LinesSent             atomic.Int64
	LinesReceived         atomic.Int64
	SlowClients           atomic.Int64
	AbuseCommandFlood     atomic.Int64
	AbuseRapidConnections atomic.Int64
	AbuseLineTooLong      atomic.Int64
}

// NewMetrics starts a counter set.
func NewMetrics() *Metrics { return &Metrics{start: time.Now()} }

// Uptime since the process started.
func (m *Metrics) Uptime() time.Duration { return time.Since(m.start) }

type metric struct {
	name, help, kind string
	value            int64
}

// WriteProm renders the counters. players is read from the game so the
// gauge is always the live value.
func (m *Metrics) WriteProm(w io.Writer, players int) {
	rows := []metric{
		{"tap_uptime_seconds", "Seconds since the server started", "gauge", int64(m.Uptime().Seconds())},
		{"tap_players_online", "Players currently in the world", "gauge", int64(players)},
		{"tap_connections_open", "Open client connections", "gauge", m.ConnectionsOpen.Load()},
		{"tap_connections_total", "Connections accepted since start", "counter", m.ConnectionsTotal.Load()},
		{"tap_connections_rejected_total", "Connections refused (server full)", "counter", m.ConnectionsRejected.Load()},
		{"tap_commands_total", "Protocol commands executed", "counter", m.Commands.Load()},
		{"tap_errors_total", "ERR replies sent", "counter", m.Errors.Load()},
		{"tap_lines_sent_total", "Protocol lines written to clients", "counter", m.LinesSent.Load()},
		{"tap_lines_received_total", "Protocol lines read from clients", "counter", m.LinesReceived.Load()},
		{"tap_slow_clients_total", "Clients dropped for not draining their queue", "counter", m.SlowClients.Load()},
		{"tap_abuse_command_flood_total", "Rate-limited commands", "counter", m.AbuseCommandFlood.Load()},
		{"tap_abuse_rapid_connections_total", "Connections refused for reconnecting too fast", "counter", m.AbuseRapidConnections.Load()},
		{"tap_abuse_line_too_long_total", "Oversized lines rejected", "counter", m.AbuseLineTooLong.Load()},
	}
	for _, r := range rows {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n%s %d\n", r.name, r.help, r.name, r.kind, r.name, r.value)
	}
}
