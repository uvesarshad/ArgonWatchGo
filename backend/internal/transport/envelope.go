// Package transport defines the wire format spoken between the hub and its
// agents, and between the hub and connected browsers. All v2 messages share
// a single envelope shape so multi-server routing is uniform:
//
//	{ "type": "SYSTEM_METRICS", "serverId": "local", "ts": 1716368400000, "payload": { ... } }
//
// v1 clients only inspected "type" and the legacy "payload" alias, so adding
// "serverId" and "ts" is a backward-compatible expansion.
package transport

import "time"

// Envelope is the canonical message shape on the wire.
type Envelope struct {
	Type     string      `json:"type"`
	ServerID string      `json:"serverId,omitempty"`
	Ts       int64       `json:"ts,omitempty"`
	Payload  interface{} `json:"payload"`
}

// New builds an envelope, stamping the current time if ts is zero.
func New(serverID, msgType string, payload interface{}) Envelope {
	return Envelope{
		Type:     msgType,
		ServerID: serverID,
		Ts:       time.Now().UnixMilli(),
		Payload:  payload,
	}
}

// Message types spoken by agents to the hub.
const (
	MsgSystemMetrics  = "SYSTEM_METRICS"
	MsgServiceStatus  = "SERVICE_STATUS"
	MsgDatabaseStats  = "DATABASE_STATS"
	MsgPM2Status      = "PM2_STATUS"
	MsgGHRunnerStatus = "GH_RUNNER_STATUS"
	MsgLogTail        = "LOG_TAIL"
	MsgTerminalOut    = "TERMINAL_OUTPUT"
	MsgHeartbeat      = "HEARTBEAT"
)

// Message types spoken by the hub to agents.
const (
	MsgTerminalIn     = "TERMINAL_INPUT"
	MsgTerminalResize = "TERMINAL_RESIZE"
	MsgExecCommand    = "EXEC_COMMAND"
	MsgReloadConfig   = "RELOAD_CONFIG"
	MsgCloseSession   = "CLOSE_SESSION"
)

// Message types spoken between hub and browser.
const (
	MsgAlertTriggered  = "ALERT_TRIGGERED"
	MsgAlertResolved   = "ALERT_RESOLVED"
	MsgHistoricalData  = "HISTORICAL_DATA"
	MsgGetHistorical   = "GET_HISTORICAL_DATA"
	MsgGHWorkflowRuns  = "GH_WORKFLOW_RUNS"
	MsgServerOnline    = "SERVER_ONLINE"
	MsgServerOffline   = "SERVER_OFFLINE"
)
