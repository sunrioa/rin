package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/policy"
)

func TestHTTPClientUsesDaemonBoundPrincipal(t *testing.T) {
	service := New(Options{
		Now:    func() time.Time { return time.UnixMilli(1_000_000) },
		Random: bytes.NewReader(sequenceBytes(512)),
	})
	principal := operationPrincipal(
		ScopeActorRead,
		ScopeActorConverse,
		ScopeActorDirect,
		ScopeActorSpeak,
		ScopeActorExecute,
		ScopeOperationCancel,
	)
	handler, err := NewHTTPHandler(service, HTTPOptions{
		Token:           testControlToken,
		ClientPrincipal: &principal,
	})
	if err != nil {
		t.Fatalf("NewHTTPHandler: %v", err)
	}
	lease := mustRegister(t, service, registration("instance.http-client"))
	if err := service.PublishWorld(
		"test.host",
		lease.LeaseID,
		worldPublication(1, "ready"),
	); err != nil {
		t.Fatalf("PublishWorld: %v", err)
	}
	client, err := NewHTTPClient(
		"http://127.0.0.1:7375",
		testControlToken,
	)
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	client.client = &http.Client{
		Transport: handlerRoundTripper{handler: handler},
	}
	ctx := context.Background()
	info, err := client.Info(ctx)
	expectedPrincipal := clonePrincipalValue(principal)
	if err != nil ||
		info.ContractVersion != ContractVersion ||
		!reflect.DeepEqual(info.Principal, expectedPrincipal) {
		t.Fatalf("Info = %#v, %v", info, err)
	}
	worlds, err := client.ListWorlds(ctx)
	if err != nil || len(worlds) != 1 || worlds[0].WorldID != "world.one" {
		t.Fatalf("ListWorlds = %#v, %v", worlds, err)
	}
	actors, err := client.ListActors(ctx, "test.host", "world.one")
	if err != nil || len(actors) != 1 || actors[0].ActorID != "actor.one" {
		t.Fatalf("ListActors = %#v, %v", actors, err)
	}
	actor, err := client.GetActor(
		ctx,
		"test.host",
		"world.one",
		"actor.one",
	)
	if err != nil || actor.ObservationSeq != 1 {
		t.Fatalf("GetActor = %#v, %v", actor, err)
	}
	update, err := client.WaitActor(ctx, WaitActorInput{
		HostID:                 "test.host",
		WorldID:                "world.one",
		ActorID:                "actor.one",
		AfterObservationSeq:    actor.ObservationSeq,
		AfterAuthorityRevision: actor.Authority.Revision,
		WaitMillis:             0,
	})
	if err != nil || update.Changed ||
		update.Actor.ObservationSeq != actor.ObservationSeq {
		t.Fatalf("WaitActor = %#v, %v", update, err)
	}
}

type handlerRoundTripper struct {
	handler http.Handler
}

func (transport handlerRoundTripper) RoundTrip(
	request *http.Request,
) (*http.Response, error) {
	response := httptest.NewRecorder()
	transport.handler.ServeHTTP(response, request)
	return response.Result(), nil
}

func TestHTTPClientRejectsInvalidOriginAndCredentials(t *testing.T) {
	for _, target := range []string{
		"https://127.0.0.1:7375",
		"http://example.com:7375",
		"http://user@127.0.0.1:7375",
		"http://127.0.0.1:7375/path",
	} {
		if _, err := NewHTTPClient(target, testControlToken); !errors.Is(err, ErrInvalid) {
			t.Fatalf("NewHTTPClient(%q) error = %v", target, err)
		}
	}
	if _, err := NewHTTPClient(
		"http://127.0.0.1:7375",
		"short",
	); !errors.Is(err, ErrInvalid) {
		t.Fatalf("short token error = %v", err)
	}
}

func TestHTTPClientCannotBypassDaemonToken(t *testing.T) {
	service := New(Options{})
	principal := readPrincipal()
	handler, err := NewHTTPHandler(service, HTTPOptions{
		Token:           testControlToken,
		ClientPrincipal: &principal,
	})
	if err != nil {
		t.Fatalf("NewHTTPHandler: %v", err)
	}
	client, err := NewHTTPClient(
		"http://127.0.0.1:7375",
		"fedcba9876543210fedcba9876543210",
	)
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	client.client = &http.Client{
		Transport: handlerRoundTripper{handler: handler},
	}
	if _, err := client.Info(
		context.Background(),
	); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Info wrong-token error = %v", err)
	}
}

func TestHTTPClientAcceptsBoundedV2ResponsesAboveLegacyLimit(t *testing.T) {
	client, err := NewHTTPClient("http://127.0.0.1:7375", testControlToken)
	if err != nil {
		t.Fatal(err)
	}
	value := string(bytes.Repeat([]byte{'a'}, 2<<20))
	client.client = &http.Client{Transport: handlerRoundTripper{
		handler: http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			response.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(response).Encode(map[string]string{"value": value})
		}),
	}}
	var output map[string]string
	if err := client.requestV2(
		context.Background(), http.MethodPost, "fixture", emptyRequest{}, &output,
	); err != nil {
		t.Fatalf("bounded V2 response: %v", err)
	}
	if output["value"] != value {
		t.Fatal("bounded V2 response was truncated")
	}
}

func TestHTTPClientRejectsResponsesAboveV2Limit(t *testing.T) {
	client, err := NewHTTPClient("http://127.0.0.1:7375", testControlToken)
	if err != nil {
		t.Fatal(err)
	}
	client.client = &http.Client{Transport: handlerRoundTripper{
		handler: http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			response.Header().Set("Content-Type", "application/json")
			_, _ = response.Write(bytes.Repeat(
				[]byte{' '}, int(defaultControlClientMaxResponseBytes)+1,
			))
		}),
	}}
	var output map[string]any
	err = client.requestV2(
		context.Background(), http.MethodPost, "fixture", emptyRequest{}, &output,
	)
	if err == nil || !strings.Contains(err.Error(), "response is too large") {
		t.Fatalf("oversized V2 response error = %v", err)
	}
}

func TestHTTPClientV2DiscoveryControllerAndActionLifecycle(t *testing.T) {
	binder, engine := actionGatewayTestComponents(t, host.RiskLow, policy.ProfileOpen)
	service, hostLease, _ := operationTestService(t, Options{PolicyEngine: engine})
	if err := service.PublishWorld(
		"test.host",
		hostLease.LeaseID,
		v2WorldPublication(binder.spec),
	); err != nil {
		t.Fatalf("PublishWorld V2: %v", err)
	}
	principal := operationPrincipal(
		ScopeActorRead,
		ScopeActorControl,
		ScopeActorExecute,
		ScopeOperationCancel,
		"rin.policy.confirm",
	)
	handler, err := NewHTTPHandler(service, HTTPOptions{
		Token:           testControlToken,
		ClientPrincipal: &principal,
	})
	if err != nil {
		t.Fatalf("NewHTTPHandler: %v", err)
	}
	client, err := NewHTTPClient("http://127.0.0.1:7375", testControlToken)
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	client.client = &http.Client{Transport: handlerRoundTripper{handler: handler}}
	ctx := context.Background()
	target := testActorControlTarget()

	observation, err := client.GetObservation(ctx, target)
	if err != nil || observation.ObservationID != "observation.actor.one.1" {
		t.Fatalf("GetObservation = %#v, %v", observation, err)
	}
	catalog, err := client.ListCapabilities(ctx, target)
	if err != nil || len(catalog.Specs) != 1 {
		t.Fatalf("ListCapabilities = %#v, %v", catalog, err)
	}
	described, err := client.DescribeCapability(ctx, DescribeCapabilityInput{
		ActorControlTarget: target,
		Capability:         binder.spec.Capability,
	})
	if err != nil || described.Digest != binder.spec.Digest {
		t.Fatalf("DescribeCapability = %#v, %v", described, err)
	}
	controller, err := client.AcquireController(ctx, AcquireControllerInput{
		ActorControlTarget: target,
		ControllerID:       "controller.gateway.one",
		LeaseTTLMillis:     5_000,
	})
	if err != nil || controller.LeaseID == "" {
		t.Fatalf("AcquireController = %#v, %v", controller, err)
	}
	current, err := client.GetController(ctx, target)
	if err != nil || current.LeaseID != controller.LeaseID {
		t.Fatalf("GetController = %#v, %v", current, err)
	}
	renewed, err := client.RenewController(ctx, RenewControllerInput{
		ActorControlTarget: target,
		LeaseID:            controller.LeaseID,
		LeaseTTLMillis:     10_000,
	})
	if err != nil || renewed.ExpiresAtUnixMillis <= controller.ExpiresAtUnixMillis {
		t.Fatalf("RenewController = %#v, %v", renewed, err)
	}

	type submitResult struct {
		operation OperationView
		err       error
	}
	submitted := make(chan submitResult, 1)
	go func() {
		operation, submitErr := client.SubmitAction(
			ctx,
			binder.input("request.http.v2", "action.http.v2"),
		)
		submitted <- submitResult{operation: operation, err: submitErr}
	}()
	delivery := pollHost(t, service, hostLease, 1)
	if len(delivery.GatewayRequests) != 1 {
		t.Fatalf("Host gateway delivery = %#v", delivery)
	}
	gateway := delivery.GatewayRequests[0].Request
	binding, err := binder.BindAction(ctx, gateway.Target, *gateway.ActionRequest)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ReportHostGatewayResult(
		"test.host",
		hostLease.LeaseID,
		HostGatewayResult{
			GatewayRequestID: gateway.GatewayRequestID,
			Binding:          &binding,
		},
	); err != nil {
		t.Fatalf("ReportHostGatewayResult: %v", err)
	}
	var operation OperationView
	select {
	case result := <-submitted:
		if result.err != nil || result.operation.Status != OperationQueued {
			t.Fatalf("SubmitAction = %#v, %v", result.operation, result.err)
		}
		operation = result.operation
	case <-time.After(time.Second):
		t.Fatal("HTTP SubmitAction did not resume")
	}
	view, err := client.GetOperation(ctx, operation.OperationID)
	if err != nil || view.OperationID != operation.OperationID {
		t.Fatalf("GetOperation = %#v, %v", view, err)
	}
	stop, err := client.SetEmergencyStop(ctx, SetEmergencyStopInput{
		ActorControlTarget: target,
		Active:             true,
	})
	if err != nil || !stop.Active {
		t.Fatalf("SetEmergencyStop = %#v, %v", stop, err)
	}
	if _, err := client.SetEmergencyStop(ctx, SetEmergencyStopInput{
		ActorControlTarget: target,
		Active:             false,
	}); err != nil {
		t.Fatalf("clear emergency stop: %v", err)
	}
	if err := client.ReleaseController(ctx, ReleaseControllerInput{
		ActorControlTarget: target,
		LeaseID:            renewed.LeaseID,
	}); err != nil {
		t.Fatalf("ReleaseController: %v", err)
	}
}

func TestHTTPClientPreservesStableServiceErrorCode(t *testing.T) {
	if err := controlClientStatusError(
		http.StatusConflict,
		"stale",
		"published actor state changed",
	); !errors.Is(err, ErrStale) {
		t.Fatalf("stable stale error = %v", err)
	}
	if err := controlClientStatusError(
		http.StatusConflict,
		"lease_conflict",
		"another Host instance is live",
	); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("stable lease conflict error = %v", err)
	}
	if err := controlClientStatusError(
		http.StatusNotFound,
		"",
		"legacy daemon response",
	); !errors.Is(err, ErrNotFound) {
		t.Fatalf("legacy status fallback error = %v", err)
	}
	if err := controlClientStatusError(
		http.StatusConflict,
		"not_accepted",
		"operation was not acknowledged",
	); !errors.Is(err, ErrNotAccepted) {
		t.Fatalf("stable not-accepted error = %v", err)
	}
}
