package main

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/pkg/access"
)

const scimEnterpriseURN = "urn:ietf:params:scim:schemas:extension:enterprise:2.0:User"
const scimGroupURN = "urn:ietf:params:scim:schemas:core:2.0:Group"
const scimUserURN = "urn:ietf:params:scim:schemas:core:2.0:User"

var errSCIMPatchPath = errors.New("unsupported SCIM PATCH path")

func scimStoreError(c *fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, errSCIMPatchPath):
		return scimErr(c, 400, "invalidPath", "unsupported PATCH path")
	case errors.Is(err, access.ErrSCIMInvalid):
		return scimErr(c, 400, "invalidValue", "invalid managed resource")
	case errors.Is(err, access.ErrSCIMConflict), errors.Is(err, access.ErrSCIMEmailConflict), errors.Is(err, access.ErrSCIMExternalIDBound):
		return scimErr(c, 409, "uniqueness", "resource identity or name already exists")
	case errors.Is(err, access.ErrSCIMNotFound), errors.Is(err, access.ErrUserNotFound):
		return scimErr(c, 404, "", "resource not found")
	case errors.Is(err, access.ErrSCIMVersionConflict):
		return scimErr(c, 412, "", "resource version changed")
	default:
		return scimErr(c, 500, "", "provisioning failed")
	}
}
func scimExpectedVersion(c *fiber.Ctx) (int64, error) {
	raw := c.Get("If-Match")
	if raw == "" || raw == "*" {
		return 0, nil
	}
	raw = strings.TrimPrefix(raw, "W/")
	if len(raw) < 3 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return 0, access.ErrSCIMInvalid
	}
	n, err := strconv.ParseInt(raw[1:len(raw)-1], 10, 64)
	if err != nil || n <= 0 {
		return 0, access.ErrSCIMInvalid
	}
	return n, nil
}
func scimGroupResource(g access.SCIMGroup) fiber.Map {
	members := []fiber.Map{}
	for _, id := range g.Members {
		members = append(members, fiber.Map{"value": id, "type": "User", "$ref": "/scim/v2/Users/" + id})
	}
	return fiber.Map{"schemas": []string{scimGroupURN}, "id": g.ID, "externalId": g.ExternalID, "displayName": g.DisplayName, "members": members, "meta": fiber.Map{"resourceType": "Group", "version": "W/\"" + strconv.FormatInt(g.Revision, 10) + "\"", "location": "/scim/v2/Groups/" + g.ID}}
}
func sendSCIMGroup(c *fiber.Ctx, g access.SCIMGroup, status int) error {
	c.Set("ETag", "W/\""+strconv.FormatInt(g.Revision, 10)+"\"")
	return c.Status(status).JSON(scimGroupResource(g))
}
func scimMembers(raw json.RawMessage) ([]string, error) {
	var values []struct {
		Value, Type string
		Ref         string `json:"$ref"`
	}
	if string(raw) == "null" || json.Unmarshal(raw, &values) != nil || len(values) > 1000 {
		return nil, access.ErrSCIMInvalid
	}
	members := make([]string, 0, len(values))
	for _, v := range values {
		if v.Value == "" || (v.Type != "" && !strings.EqualFold(v.Type, "User")) || (v.Ref != "" && v.Ref != "/scim/v2/Users/"+v.Value) {
			return nil, access.ErrSCIMInvalid
		}
		members = append(members, v.Value)
	}
	return members, nil
}

type scimPatchOperation struct {
	Op, Path string
	Value    json.RawMessage
}

func parseSCIMPatch(body []byte) ([]scimPatchOperation, error) {
	var input struct{ Operations []scimPatchOperation }
	if json.Unmarshal(body, &input) != nil || len(input.Operations) == 0 || len(input.Operations) > 100 {
		return nil, access.ErrSCIMInvalid
	}
	for i := range input.Operations {
		op := &input.Operations[i]
		op.Op = strings.ToLower(strings.TrimSpace(op.Op))
		if op.Op != "add" && op.Op != "replace" && op.Op != "remove" {
			return nil, access.ErrSCIMInvalid
		}
		if op.Op != "remove" && len(op.Value) == 0 {
			return nil, access.ErrSCIMInvalid
		}
		if op.Op == "remove" && op.Path == "" {
			return nil, access.ErrSCIMInvalid
		}
	}
	return input.Operations, nil
}
func scimGroupPatch(body []byte) ([]access.SCIMGroupOperation, error) {
	input, err := parseSCIMPatch(body)
	if err != nil {
		return nil, err
	}
	out := []access.SCIMGroupOperation{}
	var appendField func(string, string, json.RawMessage) error
	appendField = func(op, path string, value json.RawMessage) error {
		switch strings.ToLower(path) {
		case "displayname":
			var name string
			if op == "remove" || json.Unmarshal(value, &name) != nil {
				return access.ErrSCIMInvalid
			}
			out = append(out, access.SCIMGroupOperation{Op: op, Field: "displayName", Value: name})
		case "members":
			var members []string
			if op != "remove" {
				var err error
				members, err = scimMembers(value)
				if err != nil {
					return err
				}
			}
			out = append(out, access.SCIMGroupOperation{Op: op, Field: "members", Members: members})
		default:
			prefix := `members[value eq `
			if op != "remove" || !strings.HasPrefix(strings.ToLower(path), prefix) || !strings.HasSuffix(path, "]") {
				return access.ErrSCIMInvalid
			}
			var id string
			if json.Unmarshal([]byte(path[len(prefix):len(path)-1]), &id) != nil || id == "" {
				return access.ErrSCIMInvalid
			}
			out = append(out, access.SCIMGroupOperation{Op: op, Field: "member", Value: id})
		}
		return nil
	}
	for _, op := range input {
		if op.Path == "" {
			var fields map[string]json.RawMessage
			if json.Unmarshal(op.Value, &fields) != nil || len(fields) == 0 {
				return nil, access.ErrSCIMInvalid
			}
			for path, value := range fields {
				if err := appendField(op.Op, path, value); err != nil {
					return nil, err
				}
			}
		} else if err := appendField(op.Op, op.Path, op.Value); err != nil {
			return nil, err
		}
	}
	return out, nil
}
func (s *scimService) groupRoutes(g fiber.Router) {
	g.Get("/Groups", func(c *fiber.Ctx) error {
		field, value := "", ""
		if filter := c.Query("filter"); filter != "" {
			var ok bool
			for _, candidate := range []string{"displayName", "externalId"} {
				if value, ok = scimEqFilter(filter, candidate); ok {
					field = candidate
					break
				}
			}
			if field == "" {
				return scimErr(c, 400, "invalidFilter", "unsupported group filter")
			}
		}
		start, count := scimPagination(c)
		groups, total, err := s.managed.ListSCIMGroups(c.UserContext(), s.issuer, field, value, start, count)
		if err != nil {
			return scimStoreError(c, err)
		}
		resources := []fiber.Map{}
		for _, group := range groups {
			resources = append(resources, scimGroupResource(group))
		}
		return c.JSON(scimListResponse(resources, start, total))
	})
	g.Get("/Groups/:id", func(c *fiber.Ctx) error {
		group, err := s.managed.GetSCIMGroup(c.UserContext(), s.issuer, c.Params("id"))
		if err != nil {
			return scimStoreError(c, err)
		}
		return sendSCIMGroup(c, group, 200)
	})
	g.Post("/Groups", func(c *fiber.Ctx) error {
		var req struct {
			ExternalID  string          `json:"externalId"`
			DisplayName string          `json:"displayName"`
			Members     json.RawMessage `json:"members"`
		}
		if json.Unmarshal(c.Body(), &req) != nil {
			return scimStoreError(c, access.ErrSCIMInvalid)
		}
		members := []string{}
		if len(req.Members) > 0 {
			var err error
			members, err = scimMembers(req.Members)
			if err != nil {
				return scimStoreError(c, err)
			}
		}
		group, created, err := s.managed.CreateSCIMGroup(c.UserContext(), s.issuer, req.ExternalID, req.DisplayName, members)
		if err != nil {
			return scimStoreError(c, err)
		}
		status := 200
		if created {
			status = 201
		}
		return sendSCIMGroup(c, group, status)
	})
	g.Patch("/Groups/:id", func(c *fiber.Ctx) error {
		version, err := scimExpectedVersion(c)
		if err != nil {
			return scimStoreError(c, err)
		}
		ops, err := scimGroupPatch(c.Body())
		if err != nil {
			return scimStoreError(c, err)
		}
		group, err := s.managed.UpdateSCIMGroup(c.UserContext(), s.issuer, c.Params("id"), version, ops)
		if err != nil {
			return scimStoreError(c, err)
		}
		return sendSCIMGroup(c, group, 200)
	})
	g.Put("/Groups/:id", func(c *fiber.Ctx) error {
		version, err := scimExpectedVersion(c)
		if err != nil {
			return scimStoreError(c, err)
		}
		var req struct {
			ID, ExternalID string
			DisplayName    string
			Members        json.RawMessage
		}
		if json.Unmarshal(c.Body(), &req) != nil {
			return scimStoreError(c, access.ErrSCIMInvalid)
		}
		current, err := s.managed.GetSCIMGroup(c.UserContext(), s.issuer, c.Params("id"))
		if err != nil {
			return scimStoreError(c, err)
		}
		if (req.ID != "" && req.ID != current.ID) || (req.ExternalID != "" && req.ExternalID != current.ExternalID) {
			return scimErr(c, 400, "mutability", "group identity is immutable")
		}
		members := []string{}
		if len(req.Members) > 0 {
			members, err = scimMembers(req.Members)
			if err != nil {
				return scimStoreError(c, err)
			}
		}
		group, err := s.managed.UpdateSCIMGroup(c.UserContext(), s.issuer, current.ID, version, []access.SCIMGroupOperation{{Op: "replace", Field: "displayName", Value: req.DisplayName}, {Op: "replace", Field: "members", Members: members}})
		if err != nil {
			return scimStoreError(c, err)
		}
		return sendSCIMGroup(c, group, 200)
	})
	g.Delete("/Groups/:id", func(c *fiber.Ctx) error {
		version, err := scimExpectedVersion(c)
		if err == nil {
			err = s.managed.DeleteSCIMGroup(c.UserContext(), s.issuer, c.Params("id"), version)
		}
		if err != nil {
			return scimStoreError(c, err)
		}
		return c.SendStatus(204)
	})
}

func enterpriseFields(raw json.RawMessage) (map[string]*string, error) {
	values := map[string]json.RawMessage{}
	if json.Unmarshal(raw, &values) != nil || values == nil {
		return nil, access.ErrSCIMInvalid
	}
	out := map[string]*string{}
	for name, raw := range values {
		canonical := ""
		for _, field := range []string{"employeeNumber", "costCenter", "organization", "division", "department", "manager"} {
			if strings.EqualFold(name, field) {
				canonical = field
				break
			}
		}
		if canonical == "" {
			return nil, access.ErrSCIMInvalid
		}
		if string(raw) == "null" {
			out[canonical] = nil
			continue
		}
		var value string
		if canonical == "manager" {
			var manager struct {
				Value string
				Ref   string `json:"$ref"`
			}
			if json.Unmarshal(raw, &manager) != nil || (manager.Ref != "" && manager.Ref != "/scim/v2/Users/"+manager.Value) {
				return nil, access.ErrSCIMInvalid
			}
			value = manager.Value
		} else if json.Unmarshal(raw, &value) != nil {
			return nil, access.ErrSCIMInvalid
		}
		out[canonical] = &value
	}
	return out, nil
}
func enterpriseProfile(fields map[string]*string) *access.SCIMEnterpriseUser {
	p := &access.SCIMEnterpriseUser{}
	targets := map[string]*string{"employeeNumber": &p.EmployeeNumber, "costCenter": &p.CostCenter, "organization": &p.Organization, "division": &p.Division, "department": &p.Department, "manager": &p.ManagerID}
	for key, value := range fields {
		if value != nil {
			*targets[key] = *value
		}
	}
	return p
}
func (s *scimService) userResource(ctx context.Context, id, email, external string, active bool) (fiber.Map, error) {
	resource := scimUserResource(id, email, external, active)
	if s.managed == nil {
		return resource, nil
	}
	p, err := s.managed.EnterpriseUser(ctx, s.issuer, external)
	if err != nil {
		return nil, err
	}
	encoded, _ := json.Marshal(p)
	extension := fiber.Map{}
	_ = json.Unmarshal(encoded, &extension)
	if p.ManagerID != "" {
		extension["manager"] = fiber.Map{"value": p.ManagerID, "$ref": "/scim/v2/Users/" + p.ManagerID}
	}
	resource["schemas"] = []string{scimUserURN, scimEnterpriseURN}
	resource[scimEnterpriseURN] = extension
	return resource, nil
}
func (s *scimService) patchUser(c *fiber.Ctx) error {
	uid := c.Params("id")
	email, active, external, err := s.query.ByID(c.UserContext(), s.issuer, uid)
	if errors.Is(err, access.ErrUserNotFound) {
		return scimErr(c, 404, "", "user not found")
	}
	if err != nil {
		return scimStoreError(c, err)
	}
	ops, err := parseSCIMPatch(c.Body())
	if err != nil {
		return scimStoreError(c, err)
	}
	desired := access.SCIMUser{Issuer: s.issuer, ExternalID: external, Email: email, Active: active, ActiveUnchanged: true}
	var newEmail string
	apply := func(op, path string, raw json.RawMessage) error {
		switch strings.ToLower(path) {
		case "active":
			var value *bool
			if op == "remove" || json.Unmarshal(raw, &value) != nil || value == nil {
				return access.ErrSCIMInvalid
			}
			desired.Active = *value
			desired.ActiveUnchanged = false
			return nil
		case "username":
			if op == "remove" || json.Unmarshal(raw, &newEmail) != nil || strings.TrimSpace(newEmail) == "" {
				return access.ErrSCIMInvalid
			}
			newEmail = strings.TrimSpace(newEmail)
			return nil
		case "externalid":
			var value string
			if op == "remove" || json.Unmarshal(raw, &value) != nil || value != external {
				return access.ErrSCIMInvalid
			}
			return nil
		}
		if s.managed == nil {
			return errSCIMPatchPath
		}
		if desired.EnterprisePatch == nil {
			desired.EnterprisePatch = map[string]*string{}
		}
		var fields map[string]*string
		if strings.EqualFold(path, scimEnterpriseURN) {
			if op == "remove" || op == "replace" {
				for _, key := range []string{"employeeNumber", "costCenter", "organization", "division", "department", "manager"} {
					desired.EnterprisePatch[key] = nil
				}
			}
			if op == "remove" || string(raw) == "null" {
				return nil
			}
			var err error
			fields, err = enterpriseFields(raw)
			if err != nil {
				return err
			}
		} else if strings.HasPrefix(strings.ToLower(path), strings.ToLower(scimEnterpriseURN)+":") {
			key := path[len(scimEnterpriseURN)+1:]
			if op == "remove" {
				raw = json.RawMessage("null")
			}
			object, err := json.Marshal(map[string]json.RawMessage{key: raw})
			if err != nil {
				return err
			}
			fields, err = enterpriseFields(object)
			if err != nil {
				return err
			}
		} else {
			return errSCIMPatchPath
		}
		for key, value := range fields {
			desired.EnterprisePatch[key] = value
		}
		return nil
	}
	for _, op := range ops {
		if op.Path == "" {
			var fields map[string]json.RawMessage
			if json.Unmarshal(op.Value, &fields) != nil || len(fields) == 0 {
				return scimStoreError(c, access.ErrSCIMInvalid)
			}
			for path, value := range fields {
				if err := apply(op.Op, path, value); err != nil {
					return scimStoreError(c, err)
				}
			}
		} else if err := apply(op.Op, op.Path, op.Value); err != nil {
			return scimStoreError(c, err)
		}
	}
	if err := s.store.ProvisionUpdate(c.UserContext(), desired, newEmail); err != nil {
		return scimStoreError(c, err)
	}
	email, active, external, err = s.query.ByID(c.UserContext(), s.issuer, uid)
	if err != nil {
		return scimStoreError(c, err)
	}
	resource, err := s.userResource(c.UserContext(), uid, email, external, active)
	if err != nil {
		return scimStoreError(c, err)
	}
	s.audit("update", external, uid)
	return c.JSON(resource)
}

func (s *scimService) discoveryRoutes(g fiber.Router) {
	schemas := []fiber.Map{{"id": scimUserURN, "name": "User", "schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:Schema"}, "attributes": []fiber.Map{{"name": "userName", "type": "string", "required": true}, {"name": "active", "type": "boolean"}, {"name": "externalId", "type": "string", "mutability": "immutable"}}}}
	types := []fiber.Map{{"id": "User", "name": "User", "endpoint": "/Users", "schema": scimUserURN, "schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:ResourceType"}}}
	if s.managed != nil {
		attrs := []fiber.Map{}
		for _, name := range []string{"employeeNumber", "costCenter", "organization", "division", "department"} {
			attrs = append(attrs, fiber.Map{"name": name, "type": "string", "required": false})
		}
		attrs = append(attrs, fiber.Map{"name": "manager", "type": "complex", "subAttributes": []fiber.Map{{"name": "value", "type": "string"}, {"name": "$ref", "type": "reference", "mutability": "readOnly"}}})
		schemas = append(schemas, fiber.Map{"id": scimEnterpriseURN, "name": "EnterpriseUser", "schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:Schema"}, "attributes": attrs}, fiber.Map{"id": scimGroupURN, "name": "Group", "schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:Schema"}, "attributes": []fiber.Map{{"name": "displayName", "type": "string", "required": true}, {"name": "externalId", "type": "string", "required": true, "mutability": "immutable"}, {"name": "members", "type": "complex", "multiValued": true, "subAttributes": []fiber.Map{{"name": "value", "type": "string", "required": true, "mutability": "immutable"}, {"name": "type", "type": "string", "canonicalValues": []string{"User"}}}}}})
		types[0]["schemaExtensions"] = []fiber.Map{{"schema": scimEnterpriseURN, "required": false}}
		types = append(types, fiber.Map{"id": "Group", "name": "Group", "endpoint": "/Groups", "schema": scimGroupURN, "schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:ResourceType"}})
	}
	g.Get("/Schemas", func(c *fiber.Ctx) error { return c.JSON(scimListResponse(schemas, 1, len(schemas))) })
	g.Get("/Schemas/:id", func(c *fiber.Ctx) error {
		for _, resource := range schemas {
			if resource["id"] == c.Params("id") {
				return c.JSON(resource)
			}
		}
		return scimErr(c, 404, "", "schema not found")
	})
	g.Get("/ResourceTypes", func(c *fiber.Ctx) error { return c.JSON(scimListResponse(types, 1, len(types))) })
	g.Get("/ResourceTypes/:id", func(c *fiber.Ctx) error {
		for _, resource := range types {
			if resource["id"] == c.Params("id") {
				return c.JSON(resource)
			}
		}
		return scimErr(c, 404, "", "resource type not found")
	})
}
