package main

import (
	"github.com/gofiber/fiber/v2"
	vectorHttp "github.com/stek0v/levara/internal/http"
)

// registerAuthMethods describes initialized login routes, without disclosing
// provider endpoints, credentials or directory configuration to anonymous users.
func registerAuthMethods(public fiber.Router, auth vectorHttp.AuthConfig, saml, oidc bool) {
	methods := fiber.Map{"password": true, "registration": true, "directory": auth.DirectoryAuth != nil, "saml": saml, "oidc": oidc}
	public.Get("/auth/methods", func(c *fiber.Ctx) error {
		c.Set("Cache-Control", "no-store")
		return c.JSON(methods)
	})
}
