package access

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/crewjam/saml"
	"github.com/crewjam/saml/samlsp"
)

// SAMLSPConfig configures the Levara SAML 2.0 service provider (backlog A2).
type SAMLSPConfig struct {
	// EntityID is the Levara SP entity identifier. Required.
	EntityID string
	// AcsURL is the Assertion Consumer Service URL receiving IdP POSTs. Required.
	AcsURL string
	// MetadataURL is where SP metadata is served. Optional.
	MetadataURL string
	// IDPMetadataURL fetches IdP metadata over HTTPS at construction.
	// Exactly one of IDPMetadataURL / IDPMetadataXML must be set.
	IDPMetadataURL string
	// IDPMetadataXML is raw IdP metadata from a trusted local source.
	IDPMetadataXML []byte
	// Key is the SP signing key. Required.
	Key *rsa.PrivateKey
	// Certificate is the SP certificate matching Key. Required.
	Certificate *x509.Certificate
	// RootURL is the Levara base URL the SP is reachable at (used to derive
	// defaults when individual URLs are empty). Optional.
	RootURL string
}

// SAMLSP is the Levara-facing SAML 2.0 service provider. Verification
// (signature over assertion, NotBefore/NotOnOrAfter with the library's
// MaxClockSkew, audience restriction, destination, InResponseTo binding and
// replay rejection) is delegated to crewjam/saml — hand-rolling XML-DSig is
// how signature-wrapping bugs happen. This type adapts that library to the
// same IdentityBridge seam the OIDC path uses.
type SAMLSP struct {
	sp      *saml.ServiceProvider
	bridge  IdentityBridge
	pending *samlIDStore
}

// NewSAMLSP validates config, loads IdP metadata, and returns a ready SP.
// Failures abort construction: a misconfigured identity provider is a
// deployment error, not a runtime condition.
func NewSAMLSP(ctx context.Context, cfg SAMLSPConfig, bridge IdentityBridge) (*SAMLSP, error) {
	if strings.TrimSpace(cfg.EntityID) == "" || strings.TrimSpace(cfg.AcsURL) == "" {
		return nil, errors.New("saml: EntityID and AcsURL are required")
	}
	if cfg.Key == nil || cfg.Certificate == nil {
		return nil, errors.New("saml: SP key and certificate are required")
	}
	if bridge == nil {
		return nil, errors.New("saml: identity bridge is required")
	}
	var idpMeta *saml.EntityDescriptor
	switch {
	case cfg.IDPMetadataURL != "" && cfg.IDPMetadataXML != nil:
		return nil, errors.New("saml: set either IDPMetadataURL or IDPMetadataXML, not both")
	case cfg.IDPMetadataURL != "":
		u, err := url.Parse(cfg.IDPMetadataURL)
		if err != nil {
			return nil, fmt.Errorf("saml: idp metadata url: %w", err)
		}
		if u.Scheme != "https" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" {
			return nil, errors.New("saml: idp metadata must be fetched over https (or localhost for tests)")
		}
		mu, err := url.Parse(cfg.IDPMetadataURL)
		if err != nil {
			return nil, err
		}
		m, err := samlsp.FetchMetadata(ctx, http.DefaultClient, *mu)
		if err != nil {
			return nil, fmt.Errorf("saml: fetch idp metadata: %w", err)
		}
		idpMeta = m
	case cfg.IDPMetadataXML != nil:
		m, err := samlsp.ParseMetadata(cfg.IDPMetadataXML)
		if err != nil {
			return nil, fmt.Errorf("saml: parse idp metadata: %w", err)
		}
		idpMeta = m
	default:
		return nil, errors.New("saml: idp metadata is required (URL or XML)")
	}

	acs, err := url.Parse(cfg.AcsURL)
	if err != nil {
		return nil, fmt.Errorf("saml: acs url: %w", err)
	}
	sp := &saml.ServiceProvider{
		EntityID:    cfg.EntityID,
		Key:         cfg.Key,
		Certificate: cfg.Certificate,
		AcsURL:      *acs,
		IDPMetadata: idpMeta,
		// Replay protection is enforced via request-ID binding; unsolicited
		// (IdP-initiated) responses are rejected in ConsumeResponse.
		AllowIDPInitiated: false,
	}
	if cfg.MetadataURL != "" {
		mu, err := url.Parse(cfg.MetadataURL)
		if err != nil {
			return nil, fmt.Errorf("saml: metadata url: %w", err)
		}
		sp.MetadataURL = *mu
	}
	return &SAMLSP{sp: sp, bridge: bridge, pending: newSAMLIDStore(15 * time.Minute)}, nil
}

// StartAuth creates an SP-initiated AuthnRequest and returns the IdP redirect
// URL. The tracked request ID binds the eventual assertion (one-time use).
func (s *SAMLSP) StartAuth(relayState string) (string, error) {
	req, err := s.sp.MakeAuthenticationRequest(
		s.sp.GetSSOBindingLocation(saml.HTTPRedirectBinding),
		saml.HTTPRedirectBinding, saml.HTTPPostBinding)
	if err != nil {
		return "", fmt.Errorf("saml: authn request: %w", err)
	}
	s.pending.track(req.ID)
	u, err := req.Redirect(relayState, s.sp)
	if err != nil {
		return "", fmt.Errorf("saml: redirect: %w", err)
	}
	return u.String(), nil
}

// ConsumeResponse verifies a base64 SAMLResponse POSTed to the ACS and
// resolves the verified identity through the identity bridge. Every failure
// mode returns an error; callers treat the response as rejected.
func (s *SAMLSP) ConsumeResponse(ctx context.Context, r *http.Request) (Principal, error) {
	if r.PostFormValue("SAMLResponse") == "" {
		return Principal{}, errors.New("saml: missing SAMLResponse form field")
	}
	// Track the request ID the assertion must answer. PossibleRequestIDs of
	// exactly [trackedID] makes any other InResponseTo (including empty)
	// fail validation; the tracked ID is consumed atomically after success,
	// so replay of the same response fails request-ID validation.
	tracked := s.trackedRequestIDs()
	assertion, err := s.sp.ParseResponse(r, tracked)
	if err != nil {
		return Principal{}, fmt.Errorf("saml: response rejected: %w", err)
	}
	if !s.pending.consumeAll(tracked) {
		return Principal{}, errors.New("saml: request id not pending (possible replay)")
	}
	ext, err := s.identityFromAssertion(assertion)
	if err != nil {
		return Principal{}, err
	}
	principal, err := s.bridge.ResolveExternal(ctx, ext)
	if err != nil {
		return Principal{}, fmt.Errorf("saml: identity resolution: %w", err)
	}
	return principal, nil
}

// trackedRequestIDs returns the single most-recently tracked request ID (the
// AuthnRequest that initiated the browser flow now POSTing back). Levara's
// flow is strictly SP-initiated and serial per browser session, so a
// single-element allowlist is correct; anything else fails InResponseTo
// validation in the library.
func (s *SAMLSP) trackedRequestIDs() []string {
	id, ok := s.pending.latest()
	if !ok {
		return []string{""} // forces InResponseTo mismatch → reject
	}
	return []string{id}
}

// identityFromAssertion extracts NameID and attribute claims into the
// protocol-agnostic ExternalIdentity the bridge consumes.
func (s *SAMLSP) identityFromAssertion(a *saml.Assertion) (ExternalIdentity, error) {
	if a.Subject == nil || a.Subject.NameID == nil || a.Subject.NameID.Value == "" {
		return ExternalIdentity{}, errors.New("saml: assertion has no NameID")
	}
	ext := ExternalIdentity{
		Issuer:  a.Issuer.Value,
		Subject: a.Subject.NameID.Value,
	}
	for _, stmt := range a.AttributeStatements {
		for _, attr := range stmt.Attributes {
			if len(attr.Values) == 0 {
				continue
			}
			val := strings.TrimSpace(attr.Values[0].Value)
			switch attr.FriendlyName {
			case "email", "Email", "mail":
				if ext.Email == "" {
					ext.Email = val
				}
			case "displayName", "DisplayName":
				if ext.DisplayName == "" {
					ext.DisplayName = val
				}
			case "groups", "Groups", "memberOf":
				for _, v := range attr.Values {
					if g := strings.TrimSpace(v.Value); g != "" {
						ext.Groups = append(ext.Groups, g)
					}
				}
			}
		}
	}
	return ext, nil
}

// Metadata renders the SP metadata XML for IdP-side registration.
func (s *SAMLSP) Metadata() string {
	buf, _ := xml.MarshalIndent(s.sp.Metadata(), "", "  ")
	return string(buf)
}

// ── request-ID tracking (one-time use) ──

type samlIDStore struct {
	mu  sync.Mutex
	ids []trackedID
	ttl time.Duration
}

type trackedID struct {
	id      string
	expires time.Time
}

func newSAMLIDStore(ttl time.Duration) *samlIDStore {
	s := &samlIDStore{ttl: ttl}
	go func() {
		t := time.NewTicker(time.Minute)
		for range t.C {
			now := time.Now()
			s.mu.Lock()
			kept := s.ids[:0]
			for _, id := range s.ids {
				if now.Before(id.expires) {
					kept = append(kept, id)
				}
			}
			s.ids = kept
			s.mu.Unlock()
		}
	}()
	return s
}

func (s *samlIDStore) track(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ids = append(s.ids, trackedID{id: id, expires: time.Now().Add(s.ttl)})
	// Bound memory: keep at most the last 100 outstanding requests.
	if len(s.ids) > 100 {
		s.ids = s.ids[len(s.ids)-100:]
	}
}

func (s *samlIDStore) latest() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.ids) - 1; i >= 0; i-- {
		if time.Now().Before(s.ids[i].expires) {
			return s.ids[i].id, true
		}
	}
	return "", false
}

// consumeAll removes the given IDs, returning true only if at least one was
// still pending (live, not expired) — post-ParseResponse this confirms first
// use. Expired entries are treated as absent.
func (s *samlIDStore) consumeAll(ids []string) bool {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	used := false
	kept := s.ids[:0]
	for _, e := range s.ids {
		remove := false
		for _, id := range ids {
			if e.id == id {
				remove = true
				if now.Before(e.expires) {
					used = true
				}
				break
			}
		}
		if !remove {
			kept = append(kept, e)
		}
	}
	s.ids = kept
	return used
}
