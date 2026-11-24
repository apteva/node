// Package proto defines the Node link protocol: JSON frames exchanged over a
// single outbound WebSocket between a Node and its Apteva instance.
//
// Every frame is {"type": ..., "id": ..., "data": {...}}. "id" correlates an
// invoke with its result; other frames may omit it.
package proto

import (
	"encoding/json"
	"time"
)

// Version is bumped on incompatible frame changes. The server rejects a
// hello whose major version it doesn't speak.
const Version = "1"

const (
	// Node → server
	TypeHello      = "hello"
	TypeResult     = "result"
	TypeEvent      = "event"
	TypeMessage    = "message"      // the person at the device said/typed something
	TypeState      = "state"        // capability state changed (relay flipped, etc.)
	TypeAppRequest = "app_request"  // a widget on the device needs data from its app
	TypeSelect     = "select_agent" // the person picked another agent to talk to

	// Server → Node
	TypeWelcome     = "welcome"
	TypeInvoke      = "invoke"
	TypeSay         = "say"          // agent text for the device to show (and later speak)
	TypeStatus      = "status"       // agent activity: idle, thinking, acting, speaking
	TypeWidgets     = "widgets"      // full list of widgets placed on this device
	TypeAppResponse = "app_response" // answer to an app_request, same id
	TypeAppEvent    = "app_event"    // an app published a topic; refresh widgets that listen
	TypeAgents      = "agents"       // the agents this device may talk to, with their status
)

// NativeSurfaceSchema is the only widget document schema this Node renders.
// Documents are apteva-native-surface/v1 from app-sdk, unchanged.
const NativeSurfaceSchema = "apteva-native-surface/v1"

type Frame struct {
	Type string          `json:"type"`
	ID   string          `json:"id,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

func New(typ, id string, data any) (Frame, error) {
	f := Frame{Type: typ, ID: id}
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			return f, err
		}
		f.Data = b
	}
	return f, nil
}

// Hello announces the device and everything the agent may call on it.
type Hello struct {
	Protocol string `json:"protocol"`
	Name     string `json:"name"`
	Version  string `json:"version"`
	Tools    []Tool `json:"tools"`
	// Capabilities that only emit events (buttons, thresholds).
	Events []EventSpec `json:"events"`
	// UI documents this Node can draw, e.g. "apteva-native-surface/v1#widget".
	Renders []string `json:"renders"`
}

// Tool is shaped to map 1:1 onto an MCP tool definition server-side; the
// server prefixes the name with the node's name.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
	// Confirm tells the server a human must approve on the device, so it
	// can tell the agent to expect a delay or a denial.
	Confirm  string `json:"confirm,omitempty"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

type EventSpec struct {
	Capability  string `json:"capability"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
}

type Welcome struct {
	DeviceID  string `json:"device_id,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
	AgentID   string `json:"agent_id,omitempty"`
	Agent     string `json:"agent,omitempty"` // display name of the bound agent
	Icon      string `json:"icon,omitempty"`  // the agent's dashboard icon id (robot, assistant, …)
}

type Invoke struct {
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args,omitempty"`
}

type Result struct {
	OK    bool   `json:"ok"`
	Data  any    `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
	// Denied is set when a human (or the confirm timeout) refused.
	Denied bool `json:"denied,omitempty"`
}

type Event struct {
	ID         string    `json:"event_id"`
	Capability string    `json:"capability"`
	Kind       string    `json:"kind"`
	Data       any       `json:"data,omitempty"`
	At         time.Time `json:"at"`
}

type Message struct {
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

type Say struct {
	Text string `json:"text"`
}

type Status struct {
	State  string `json:"state"` // idle | thinking | acting | speaking
	Detail string `json:"detail,omitempty"`
}

type State struct {
	Capability string `json:"capability"`
	Value      any    `json:"value"`
}

// Widget is one app widget placed on the device. Surface is the app's
// native-surface document verbatim; Settings are already merged with the
// manifest defaults by the server.
type Widget struct {
	ID            string          `json:"id"`  // placement id, unique on this device
	App           string          `json:"app"` // app name; requests route to it
	Component     string          `json:"component"`
	Size          string          `json:"size"` // half | full
	Settings      map[string]any  `json:"settings,omitempty"`
	RefreshTopics []string        `json:"refresh_topics,omitempty"`
	Surface       json.RawMessage `json:"surface"`
}

type Widgets struct {
	Widgets []Widget `json:"widgets"`
}

// AppRequest is a native-surface data source or action request. The server
// resolves Path inside App only and adds the device's project scope; the
// Node never holds app credentials.
type AppRequest struct {
	ProjectID string         `json:"project_id"`
	App       string         `json:"app"`
	Method    string         `json:"method"`
	Path      string         `json:"path"`
	Query     map[string]any `json:"query,omitempty"`
	Body      any            `json:"body,omitempty"`
}

type AppResponse struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body,omitempty"`
	Error  string          `json:"error,omitempty"`
}

type AppEvent struct {
	App   string `json:"app"`
	Topic string `json:"topic"`
}

// Agents lists every agent the paired account may access across projects.
// The server sends it after welcome and whenever one of them changes.
type Agents struct {
	Agents []AgentInfo `json:"agents"`
}

type AgentInfo struct {
	ProjectID   string `json:"project_id,omitempty"`
	ProjectName string `json:"project_name,omitempty"`
	ID          string `json:"id"`
	Name        string `json:"name"`
	Icon        string `json:"icon,omitempty"`   // dashboard icon id
	Role        string `json:"role,omitempty"`   // one line: what it's for
	State       string `json:"state"`            // idle | thinking | acting | speaking | paused
	Detail      string `json:"detail,omitempty"` // what it is doing right now
}

// Select asks the server to bind this device to another agent. The server
// answers with a new welcome (or ignores an agent the device may not use).
type Select struct {
	AgentID string `json:"agent_id"`
}

// Projects is the account-wide project directory supplied by the server.
type ProjectInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type Projects struct {
	Projects []ProjectInfo `json:"projects"`
}
