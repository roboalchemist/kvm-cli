package ws

import (
	"encoding/json"
	"fmt"
	"time"
)

// Resolution is the pixel resolution of the streamed video source.
type Resolution struct {
	Width  int `json:"width" yaml:"width"`
	Height int `json:"height" yaml:"height"`
}

// HIDState mirrors the parts of the device's HID state that the agent needs:
// keyboard LEDs, mouse mode and output.
type HIDState struct {
	Enabled        bool     `json:"enabled" yaml:"enabled"`
	Online         bool     `json:"online" yaml:"online"`
	Busy           bool     `json:"busy" yaml:"busy"`
	Connected      bool     `json:"connected" yaml:"connected"`
	KeyboardOnline bool     `json:"keyboard_online" yaml:"keyboard_online"`
	MouseOnline    bool     `json:"mouse_online" yaml:"mouse_online"`
	Absolute       bool     `json:"absolute" yaml:"absolute"`
	MouseOutput    string   `json:"mouse_output" yaml:"mouse_output"`
	MouseOutputs   []string `json:"mouse_outputs,omitempty" yaml:"mouse_outputs,omitempty"`
	CapsLock       bool     `json:"caps_lock" yaml:"caps_lock"`
	NumLock        bool     `json:"num_lock" yaml:"num_lock"`
	ScrollLock     bool     `json:"scroll_lock" yaml:"scroll_lock"`
}

// StreamerState mirrors the parts of the device's streamer state the agent
// needs: whether the video pipeline is online and the negotiated resolution.
type StreamerState struct {
	Online      bool       `json:"online" yaml:"online"`
	H264Online  bool       `json:"h264_online" yaml:"h264_online"`
	JPEGClients bool       `json:"jpeg_clients" yaml:"jpeg_clients"`
	H264Clients bool       `json:"h264_clients" yaml:"h264_clients"`
	Resolution  Resolution `json:"resolution" yaml:"resolution"`
}

// State is the latest server state observed over the WebSocket. Every field is
// a zero value until the corresponding event has been received.
type State struct {
	// ProtocolVersion is the kvmd protocol version reported by the "loop" event.
	ProtocolVersion string `json:"protocol_version,omitempty" yaml:"protocol_version,omitempty"`
	// KVMVersion is the kvmd daemon version (from "info" events), when seen.
	KVMVersion string `json:"kvm_version,omitempty" yaml:"kvm_version,omitempty"`

	HID      HIDState      `json:"hid" yaml:"hid"`
	HIDSeen  bool          `json:"hid_seen" yaml:"hid_seen"`
	Streamer StreamerState `json:"streamer" yaml:"streamer"`
	// StreamerSeen reports whether any "streamer" event has been received.
	StreamerSeen bool `json:"streamer_seen" yaml:"streamer_seen"`

	// HasResolution is true once a non-zero streamer resolution has been seen.
	HasResolution bool `json:"has_resolution" yaml:"has_resolution"`

	LastPong  time.Time `json:"last_pong,omitempty" yaml:"last_pong,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty" yaml:"updated_at,omitempty"`
}

// Resolution returns the last known stream resolution and whether it is known.
func (s State) GetResolution() (Resolution, bool) {
	return s.Streamer.Resolution, s.HasResolution
}

// inboundMessage is the common JSON shape of every server event.
type inboundMessage struct {
	EventType string          `json:"event_type"`
	Event     json.RawMessage `json:"event"`
}

// hidEvent is the payload of an "hid" event.
type hidEvent struct {
	Enabled   bool `json:"enabled"`
	Online    bool `json:"online"`
	Busy      bool `json:"busy"`
	Connected bool `json:"connected"`
	Keyboard  struct {
		Online bool `json:"online"`
		Leds   struct {
			Caps   bool `json:"caps"`
			Num    bool `json:"num"`
			Scroll bool `json:"scroll"`
		} `json:"leds"`
		Outputs struct {
			Active    string   `json:"active"`
			Available []string `json:"available"`
		} `json:"outputs"`
	} `json:"keyboard"`
	Mouse struct {
		Online   bool `json:"online"`
		Absolute bool `json:"absolute"`
		Outputs  struct {
			Active    string   `json:"active"`
			Available []string `json:"available"`
		} `json:"outputs"`
	} `json:"mouse"`
}

// streamerEvent is the payload of a "streamer" event.
type streamerEvent struct {
	Streamer *struct {
		Encoder struct {
			Type string `json:"type"`
		} `json:"encoder"`
		H264 struct {
			Online bool `json:"online"`
			FPS    int  `json:"fps"`
		} `json:"h264"`
		Sinks struct {
			JPEG struct {
				HasClients bool `json:"has_clients"`
			} `json:"jpeg"`
			H264 struct {
				HasClients bool `json:"has_clients"`
			} `json:"h264"`
		} `json:"sinks"`
		Source struct {
			Resolution Resolution `json:"resolution"`
		} `json:"source"`
	} `json:"streamer"`
}

// infoEvent is the payload of an "info" event. Only a few nested fields are
// modelled; the rest is ignored.
type infoEvent struct {
	System *struct {
		KVMD struct {
			Version string `json:"version"`
		} `json:"kvmd"`
	} `json:"system"`
}

// loopEvent is the payload of a "loop" event.
type loopEvent struct {
	Version struct {
		Major int `json:"major"`
		Minor int `json:"minor"`
	} `json:"version"`
}

// applyMessage parses a server message and folds it into the state. It returns
// true when the state changed (so a handler can be notified).
func (c *Client) applyMessage(data []byte) bool {
	var msg inboundMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		c.debugf("ws: ignoring unparseable message (%d bytes): %v", len(data), err)
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	changed := false
	switch msg.EventType {
	case "loop":
		var e loopEvent
		if json.Unmarshal(msg.Event, &e) == nil {
			c.state.ProtocolVersion = trimVersion(e.Version.Major, e.Version.Minor)
			changed = true
		}
	case "hid":
		var e hidEvent
		if json.Unmarshal(msg.Event, &e) == nil {
			c.state.HID = HIDState{
				Enabled:        e.Enabled,
				Online:         e.Online,
				Busy:           e.Busy,
				Connected:      e.Connected,
				KeyboardOnline: e.Keyboard.Online,
				MouseOnline:    e.Mouse.Online,
				Absolute:       e.Mouse.Absolute,
				MouseOutput:    e.Mouse.Outputs.Active,
				MouseOutputs:   e.Mouse.Outputs.Available,
				CapsLock:       e.Keyboard.Leds.Caps,
				NumLock:        e.Keyboard.Leds.Num,
				ScrollLock:     e.Keyboard.Leds.Scroll,
			}
			c.state.HIDSeen = true
			changed = true
		}
	case "streamer":
		var e streamerEvent
		if json.Unmarshal(msg.Event, &e) == nil && e.Streamer != nil {
			s := StreamerState{
				Online:      e.Streamer.H264.Online,
				H264Online:  e.Streamer.H264.Online,
				JPEGClients: e.Streamer.Sinks.JPEG.HasClients,
				H264Clients: e.Streamer.Sinks.H264.HasClients,
				Resolution:  e.Streamer.Source.Resolution,
			}
			c.state.Streamer = s
			c.state.StreamerSeen = true
			if s.Resolution.Width > 0 && s.Resolution.Height > 0 {
				c.state.HasResolution = true
			}
			changed = true
		}
	case "info":
		var e infoEvent
		if json.Unmarshal(msg.Event, &e) == nil && e.System != nil {
			c.state.KVMVersion = e.System.KVMD.Version
			changed = true
		}
	case "pong":
		c.state.LastPong = time.Now()
		changed = true
	}

	if changed {
		c.state.UpdatedAt = time.Now()
	}

	if changed && c.onState != nil {
		// Invoke the callback without holding the lock to avoid deadlocks when
		// the callback itself calls back into the client.
		snapshot := c.state
		handler := c.onState
		c.mu.Unlock()
		handler(snapshot)
		c.mu.Lock()
	}
	return changed
}

func trimVersion(major, minor int) string {
	if major == 0 && minor == 0 {
		return ""
	}
	return fmt.Sprintf("%d.%d", major, minor)
}
