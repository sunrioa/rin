package api

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"
)

func TestSignalContractRoutes(t *testing.T) {
	routes, err := ParseSignalRoutes()
	if err != nil {
		t.Fatal(err)
	}
	want := []Route{
		{OperationID: "signals_v1_publish_host", Method: "POST", Path: "/signals/v1/host/publish", SuccessStatus: 200},
		{OperationID: "signals_v1_configure_host", Method: "POST", Path: "/signals/v1/host/settings", SuccessStatus: 200},
		{OperationID: "signals_v1_list", Method: "POST", Path: "/signals/v1/list", SuccessStatus: 200},
		{OperationID: "signals_v1_wait", Method: "POST", Path: "/signals/v1/wait", SuccessStatus: 200},
	}
	if !slices.Equal(routes, want) {
		t.Fatalf("routes = %#v, want %#v", routes, want)
	}
	var document map[string]any
	if err := json.Unmarshal(SignalDocument(), &document); err != nil {
		t.Fatal(err)
	}
	first := SignalDocument()
	first[0] = 'x'
	if bytes.Equal(first, SignalDocument()) {
		t.Fatal("SignalDocument returned shared storage")
	}
}
