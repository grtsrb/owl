package server

import "testing"

// A version pin is per component: as the global policy it would make one version every component's target.
func TestGlobalPinRefused(t *testing.T) {
	e := newSigninEnv(t)
	admin := asAdmin(t, e.srv.URL)
	if code, body := doJSON(t, admin, "PUT", e.srv.URL+"/api/v1/policies/global", `{"policy":"pin:1.30"}`); code != 400 {
		t.Errorf("global pin: %d %v", code, body)
	}
	if code, body := doJSON(t, admin, "PUT", e.srv.URL+"/api/v1/policies/redis", `{"policy":"pin:7.2"}`); code != 200 {
		t.Errorf("component pin: %d %v", code, body)
	}
	if code, body := doJSON(t, admin, "PUT", e.srv.URL+"/api/v1/policies/global", `{"policy":"n-1"}`); code != 200 {
		t.Errorf("global n-1: %d %v", code, body)
	}
}
