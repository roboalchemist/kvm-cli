package api

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// envelope is the standard GLKVM response wrapper:
//
//	{"ok": true, "result": ...}
//	{"ok": false, "result": {"error": "...", "error_msg": "..."}}
type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
}

// apiErrorPayload is the shape of result when ok is false.
type apiErrorPayload struct {
	Error    string `json:"error"`
	ErrorMsg string `json:"error_msg"`
}

// RawMap is a generic JSON object used for endpoint payloads whose exact shape
// is not yet modelled. It is the recommended fallback for deeply unknown
// nested data.
type RawMap map[string]any

// ---- auth ---------------------------------------------------------------

// LoginResult is returned by POST /api/auth/login.
type LoginResult struct {
	Token                  string `json:"token"`
	FailedSinceLastSuccess int    `json:"failed_since_last_success"`
}

// ---- info ---------------------------------------------------------------

// Info is returned by GET /api/info. The device reports PiKVM-style daemon and
// authentication metadata. The full decoded object is always available in Raw
// even when a field is not modelled here.
//
// Live shape (firmware V1.10.1):
//
//	{"auth": {"enabled": true}, "extras": {"ipmi": {...}, "janus": {...}}}
type Info struct {
	Auth   map[string]any `json:"auth,omitempty"`
	Extras map[string]any `json:"extras,omitempty"`
	Raw    RawMap         `json:"-"`
}

// UnmarshalJSON decodes the modelled fields and also retains every key in Raw.
func (i *Info) UnmarshalJSON(data []byte) error {
	type alias Info
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*i = Info(a)

	var raw RawMap
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	i.Raw = raw
	return nil
}

// ---- capability ---------------------------------------------------------

// Capability is returned by GET /api/system/capability.
//
// Live shape (firmware V1.10.1):
//
//	{"capability": {"cpu_model": "rv1126b", ...}, "success": true}
type Capability struct {
	Capability map[string]any `json:"capability,omitempty"`
	Success    bool           `json:"success,omitempty"`
	Raw        RawMap         `json:"-"`
}

// UnmarshalJSON decodes the modelled fields and retains the full object in Raw.
func (c *Capability) UnmarshalJSON(data []byte) error {
	type alias Capability
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*c = Capability(a)

	var raw RawMap
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	c.Raw = raw
	return nil
}

// ---- system -------------------------------------------------------------

// SystemParam is returned by GET /api/system/get_param and accepted by
// /api/system/set_param. The device returns a flat object of parameter
// name/value pairs.
type SystemParam map[string]any

// OTGFunctions is returned by GET /api/system/otg_functions.
//
// Live shape (firmware V1.10.1):
//
//	{"apply_error": null, "applying": false, "enable_camera": false,
//
// "enable_keyboard": true, "enable_mic": false, "enable_mouse": true,
// "enable_mouse_alt": true, "enable_mtp": false, "ready": true,
// "start_cdrom": false, "start_flash": false}
type OTGFunctions struct {
	Applying       bool   `json:"applying"`
	ApplyError     any    `json:"apply_error,omitempty"`
	EnableCamera   bool   `json:"enable_camera"`
	EnableKeyboard bool   `json:"enable_keyboard"`
	EnableMic      bool   `json:"enable_mic"`
	EnableMouse    bool   `json:"enable_mouse"`
	EnableMouseAlt bool   `json:"enable_mouse_alt"`
	EnableMTP      bool   `json:"enable_mtp"`
	Ready          bool   `json:"ready"`
	StartCdrom     bool   `json:"start_cdrom"`
	StartFlash     bool   `json:"start_flash"`
	Raw            RawMap `json:"-"`
}

// UnmarshalJSON decodes the modelled fields and retains the full object in Raw.
func (o *OTGFunctions) UnmarshalJSON(data []byte) error {
	type alias OTGFunctions
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*o = OTGFunctions(a)

	var raw RawMap
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	o.Raw = raw
	return nil
}

// NetworkConfig is returned by GET /api/system/get_network_config.
//
// Live shape (firmware V1.10.1):
//
//	{"config": {"dns_servers": ["192.168.1.1"], "gateway": "...",
//
// "interface": "eth0", "ip_address": "...", "is_dhcp": true,
// "mac_address": "...", "netmask": "...", "state": "online"},
// "success": true}
type NetworkConfig struct {
	Config  NetworkConfigDetails `json:"config"`
	Success bool                 `json:"success,omitempty"`
	Raw     RawMap               `json:"-"`
}

// NetworkConfigDetails holds the nested "config" object of NetworkConfig.
type NetworkConfigDetails struct {
	DNSServers []string `json:"dns_servers,omitempty"`
	Gateway    string   `json:"gateway,omitempty"`
	Interface  string   `json:"interface,omitempty"`
	IPAddress  string   `json:"ip_address,omitempty"`
	IsDHCP     *bool    `json:"is_dhcp,omitempty"`
	MACAddress string   `json:"mac_address,omitempty"`
	Netmask    string   `json:"netmask,omitempty"`
	State      string   `json:"state,omitempty"`
	Raw        RawMap   `json:"-"`
}

// UnmarshalJSON decodes the modelled fields and retains the full object in Raw.
func (n *NetworkConfig) UnmarshalJSON(data []byte) error {
	type alias NetworkConfig
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*n = NetworkConfig(a)

	var raw RawMap
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	n.Raw = raw
	return nil
}

// UnmarshalJSON decodes the modelled fields and retains the nested object in Raw.
func (d *NetworkConfigDetails) UnmarshalJSON(data []byte) error {
	type alias NetworkConfigDetails
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*d = NetworkConfigDetails(a)

	var raw RawMap
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	d.Raw = raw
	return nil
}

// Hostname is returned by GET /api/system/get_hostname.
//
// Live shape (firmware V1.10.1):
//
//	{"hostname": "glkvm", "success": true}
//
// A bare JSON string is also accepted.
type Hostname struct {
	Hostname string `json:"hostname"`
	Success  bool   `json:"success,omitempty"`
}

// UnmarshalJSON accepts either an object or a bare JSON string.
func (h *Hostname) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		h.Hostname = s
		return nil
	}
	var obj struct {
		Hostname string `json:"hostname"`
		Success  bool   `json:"success"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		// Do not embed the raw payload: it may carry secret-named fields. The
		// client's redactDecodeError is the safety net, but the custom
		// unmarshaler must not leak the body in the first place.
		return fmt.Errorf("hostname: unexpected payload: %w", err)
	}
	h.Hostname = obj.Hostname
	h.Success = obj.Success
	return nil
}

// ---- upgrade ------------------------------------------------------------

// UpgradeVersion is returned by GET /api/upgrade/version, for example
// {"model": "RM1PE", "version": "V1.10.1 release2"}.
type UpgradeVersion struct {
	Model   string `json:"model"`
	Version string `json:"version"`
}

// ---- wol ----------------------------------------------------------------

// WOLList is returned by GET /api/wol/list.
//
// Live shape (firmware V1.10.1): {"devices": [...]}. A bare JSON array is also
// accepted for robustness.
type WOLList struct {
	Devices []WOLDevice `json:"devices"`
}

// UnmarshalJSON accepts either {"devices": [...]} or a bare array.
func (l *WOLList) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var devices []WOLDevice
		if err := json.Unmarshal(trimmed, &devices); err != nil {
			return err
		}
		l.Devices = devices
		return nil
	}
	var obj struct {
		Devices []WOLDevice `json:"devices"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	l.Devices = obj.Devices
	return nil
}

// WOLDevice is an element of the array in WOLList. Because the device has used
// several field spellings, both mac and mac_address are decoded; Raw retains
// the full object.
type WOLDevice struct {
	ID        int    `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	MAC       string `json:"mac,omitempty"`
	MACAddr   string `json:"mac_address,omitempty"`
	Broadcast string `json:"broadcast,omitempty"`
	IP        string `json:"ip,omitempty"`
	Port      int    `json:"port,omitempty"`
	Raw       RawMap `json:"-"`
}

// UnmarshalJSON decodes the modelled fields and retains the full object in Raw.
func (w *WOLDevice) UnmarshalJSON(data []byte) error {
	type alias WOLDevice
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*w = WOLDevice(a)

	var raw RawMap
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	w.Raw = raw
	return nil
}

// MACAddress returns whichever MAC field the device populated.
func (w *WOLDevice) MACAddress() string {
	if w.MAC != "" {
		return w.MAC
	}
	return w.MACAddr
}
