package api

import (
	"encoding/json"
	"strings"
	"testing"
)

// jsonUnmarshalErr asserts that data cannot be decoded into a fresh T.
func jsonUnmarshalErr[T any](t *testing.T, data string) {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(data), &v); err == nil {
		t.Fatalf("expected error decoding %q into %T, got %+v", data, v, v)
	}
}

func TestTypeUnmarshalInvalidJSON(t *testing.T) {
	const bad = `{"unterminated":`
	jsonUnmarshalErr[Info](t, bad)
	jsonUnmarshalErr[Capability](t, bad)
	jsonUnmarshalErr[OTGFunctions](t, bad)
	jsonUnmarshalErr[NetworkConfig](t, bad)
	jsonUnmarshalErr[NetworkConfigDetails](t, bad)
	jsonUnmarshalErr[WOLList](t, bad)
	jsonUnmarshalErr[WOLDevice](t, bad)

	// A JSON number cannot decode into these object-shaped types.
	jsonUnmarshalErr[Info](t, `42`)
	jsonUnmarshalErr[Capability](t, `42`)
	jsonUnmarshalErr[OTGFunctions](t, `42`)
	jsonUnmarshalErr[NetworkConfig](t, `42`)
	jsonUnmarshalErr[NetworkConfigDetails](t, `42`)
	jsonUnmarshalErr[WOLDevice](t, `42`)
}

func TestHostnameUnmarshalInvalid(t *testing.T) {
	// Neither a JSON string nor an object: the object fallback must fail.
	jsonUnmarshalErr[Hostname](t, `[1,2]`)
	jsonUnmarshalErr[Hostname](t, `123`)

	// The object form still succeeds and populates both fields.
	var h Hostname
	if err := json.Unmarshal([]byte(`{"hostname":"glkvm","success":true}`), &h); err != nil {
		t.Fatalf("object form: %v", err)
	}
	if h.Hostname != "glkvm" || !h.Success {
		t.Fatalf("object form decoded %+v", h)
	}
}

// TestHostnameUnmarshalErrorDoesNotEchoPayload is the D9 follow-up: the custom
// unmarshaler must not embed the raw payload (which can carry secrets) in its
// error text. The client also redacts the composed message, but the type must
// not leak in the first place.
func TestHostnameUnmarshalErrorDoesNotEchoPayload(t *testing.T) {
	var h Hostname
	err := json.Unmarshal([]byte(`{"hostname":123,"ssl_key":"SECRET_LEAK","password":"SECRET_LEAK"}`), &h)
	if err == nil {
		t.Fatal("expected a decode error for a numeric hostname")
	}
	if strings.Contains(err.Error(), "SECRET_LEAK") {
		t.Fatalf("hostname unmarshal error echoed the raw payload:\n%v", err)
	}
}

func TestWOLListUnmarshalErrors(t *testing.T) {
	// Bare array with an undecodable element.
	jsonUnmarshalErr[WOLList](t, `[{"name":"ok"}, 5]`)
	// Object form with an undecodable devices value.
	jsonUnmarshalErr[WOLList](t, `{"devices":5}`)
	// A bare scalar is neither an array nor an object.
	jsonUnmarshalErr[WOLList](t, `5`)
}

func TestWOLDeviceMACAddressPrimaryField(t *testing.T) {
	d := WOLDevice{MAC: "AA:BB:CC", MACAddr: "11:22:33"}
	if got := d.MACAddress(); got != "AA:BB:CC" {
		t.Fatalf("MACAddress() = %q, want AA:BB:CC (mac wins)", got)
	}
	d = WOLDevice{MACAddr: "11:22:33"}
	if got := d.MACAddress(); got != "11:22:33" {
		t.Fatalf("MACAddress() = %q, want 11:22:33", got)
	}
}

func TestInfoUnmarshalNull(t *testing.T) {
	var info Info
	if err := json.Unmarshal([]byte(`null`), &info); err != nil {
		t.Fatalf("null: %v", err)
	}
	if info.Auth != nil || info.Extras != nil || info.Raw != nil {
		t.Fatalf("null should leave zero fields: %+v", info)
	}
}

func TestNetworkConfigDetailsRawRetention(t *testing.T) {
	var d NetworkConfigDetails
	body := `{"ip_address":"10.0.0.2","extra_key":"extra_value"}`
	if err := json.Unmarshal([]byte(body), &d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if d.IPAddress != "10.0.0.2" {
		t.Fatalf("ip = %q", d.IPAddress)
	}
	if d.Raw["extra_key"] != "extra_value" {
		t.Fatalf("raw retention failed: %+v", d.Raw)
	}
}

func TestCapabilityAndOTGRawRetention(t *testing.T) {
	var c Capability
	if err := json.Unmarshal([]byte(`{"capability":{"x":1}}`), &c); err != nil {
		t.Fatalf("capability: %v", err)
	}
	if c.Raw["capability"] == nil {
		t.Fatalf("capability raw missing: %+v", c.Raw)
	}

	var o OTGFunctions
	if err := json.Unmarshal([]byte(`{"ready":true,"custom_field":7}`), &o); err != nil {
		t.Fatalf("otg: %v", err)
	}
	if !o.Ready || o.Raw["custom_field"] != float64(7) {
		t.Fatalf("otg raw missing: %+v", o.Raw)
	}

	var w WOLDevice
	if err := json.Unmarshal([]byte(`{"name":"n","vendor":"acme"}`), &w); err != nil {
		t.Fatalf("wol device: %v", err)
	}
	if w.Name != "n" || w.Raw["vendor"] != "acme" {
		t.Fatalf("wol device raw missing: %+v", w.Raw)
	}
}
