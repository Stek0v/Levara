package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	vectorHttp "github.com/stek0v/levara/internal/http"
)

type methodsDirectoryStub struct{}

func (methodsDirectoryStub) AuthenticateCredentials(context.Context, string, string) (vectorHttp.ExternalPrincipal, error) {
	return vectorHttp.ExternalPrincipal{}, nil
}

func TestAuthMethodsReflectInitializedRoutes(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "enterprise"}[enabled], func(t *testing.T) {
			auth := vectorHttp.AuthConfig{}
			if enabled {
				auth.DirectoryAuth = methodsDirectoryStub{}
			}
			app := fiber.New()
			registerAuthMethods(app.Group("/api/v1"), auth, enabled, enabled)
			res, err := app.Test(httptest.NewRequest("GET", "/api/v1/auth/methods", nil))
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			var got map[string]bool
			if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
				t.Fatal(err)
			}
			if res.StatusCode != 200 || res.Header.Get("Cache-Control") != "no-store" || len(got) != 5 || !got["password"] || !got["registration"] || got["directory"] != enabled || got["saml"] != enabled || got["oidc"] != enabled {
				t.Fatalf("methods=%v status=%d headers=%v", got, res.StatusCode, res.Header)
			}
		})
	}
}
