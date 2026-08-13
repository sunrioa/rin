package skillapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/host"
)

const testToken = "0123456789abcdef0123456789abcdef"

func TestHTTPClientSharesLearnedSkillCatalog(t *testing.T) {
	catalog, learned, err := cognition.OpenDefaultSkillCatalog(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(catalog, learned)
	if err != nil {
		t.Fatal(err)
	}
	principal := host.Principal{
		ID: "player.one", GrantedScopes: []string{ScopeSkillRead, ScopeSkillWrite},
	}
	handler, err := NewHTTPHandler(service, HTTPOptions{
		Token: testToken, Principal: principal,
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewHTTPClient("http://127.0.0.1:7375", testToken)
	if err != nil {
		t.Fatal(err)
	}
	client.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Result(), nil
	})

	stored, err := client.Save(context.Background(), SaveInput{
		SkillID: "collect.logs", Description: "Collect nearby logs",
		Instructions: "Observe the current world, then use published collection capabilities.",
		Adapters:     []string{"minecraft"}, Capabilities: []string{"resource.harvest"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stored.Skill.SkillID != "collect.logs" || stored.Skill.Source != "learned" ||
		stored.Skill.Version != "v1" {
		t.Fatalf("stored skill = %#v", stored.Skill)
	}
	listed, err := client.List(context.Background(), ListInput{
		Adapter: "minecraft", AvailableCapabilities: []string{"resource.harvest"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Skills) != 1 || listed.Skills[0].SkillID != "collect.logs" {
		t.Fatalf("listed skills = %#v", listed.Skills)
	}
	loaded, err := client.Get(context.Background(), GetInput{
		SkillID: "collect.logs", Version: "v1",
	})
	if err != nil || loaded.Skill.Digest != stored.Skill.Digest {
		t.Fatalf("loaded skill = %#v, err = %v", loaded.Skill, err)
	}
	if result, err := client.Reload(context.Background()); err != nil || !result.Reloaded {
		t.Fatalf("reload = %#v, %v", result, err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestHTTPHandlerEnforcesSkillWriteScope(t *testing.T) {
	catalog, learned, err := cognition.OpenDefaultSkillCatalog(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	service, _ := New(catalog, learned)
	handler, err := NewHTTPHandler(service, HTTPOptions{
		Token:     testToken,
		Principal: host.Principal{ID: "player.one", GrantedScopes: []string{ScopeSkillRead}},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/skills/v1/save",
		strings.NewReader(`{"skill_id":"collect.logs","description":"Collect logs","instructions":"Use current observations."}`),
	)
	request.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}
