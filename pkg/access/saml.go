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

// SAMLSP is the Levara-facing SAML 2.0 service provider. XML signatures,
// assertion times, audience, destination and InResponseTo are verified by
// crewjam/saml. Signed browser state selects the exact expected request, and
// the local pending store enforces one-time consumption. Verified identities
// use the same IdentityBridge seam as OIDC.
type SAMLSP struct {
	sp      *saml.ServiceProvider
	bridge  IdentityBridge
	pending *samlIDStore
	tracker samlsp.CookieRequestTracker
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
	if acs.Scheme != "https" || acs.Hostname() == "" || acs.User != nil || acs.Fragment != "" {
		return nil, errors.New("saml: ACS must be an absolute HTTPS URL without userinfo or fragment")
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
	const requestTTL = 15 * time.Minute
	opts := samlsp.Options{URL: *acs, Key: cfg.Key, CookieSameSite: http.SameSiteNoneMode}
	tracker := samlsp.DefaultRequestTracker(opts, sp)
	tracker.NamePrefix = "__Secure-levara_saml_"
	tracker.MaxAge = requestTTL
	codec := samlsp.DefaultTrackedRequestCodec(opts)
	codec.MaxAge = requestTTL
	tracker.Codec = codec
	return &SAMLSP{sp: sp, bridge: bridge, pending: newSAMLIDStore(requestTTL), tracker: tracker}, nil
}

// StartAuth creates an SP-initiated AuthnRequest and returns the IdP redirect
// URL. A signed, per-request browser cookie binds the random RelayState to
// the exact AuthnRequest; callers must send the Set-Cookie header to the browser.
func (s *SAMLSP) StartAuth(w http.ResponseWriter, r *http.Request) (string, error) {
	req, err := s.sp.MakeAuthenticationRequest(
		s.sp.GetSSOBindingLocation(saml.HTTPRedirectBinding),
		saml.HTTPRedirectBinding, saml.HTTPPostBinding)
	if err != nil {
		return "", fmt.Errorf("saml: authn request: %w", err)
	}
	relayState, err := s.tracker.TrackRequest(w, r, req.ID)
	if err != nil {
		return "", fmt.Errorf("saml: request tracking: %w", err)
	}
	u, err := req.Redirect(relayState, s.sp)
	if err != nil {
		return "", fmt.Errorf("saml: redirect: %w", err)
	}
	s.pending.track(req.ID)
	return u.String(), nil
}

// ConsumeResponse verifies a base64 SAMLResponse POSTed to the ACS and
// resolves the verified identity through the identity bridge. Every failure
// mode returns an error; callers treat the response as rejected.
func (s *SAMLSP) ConsumeResponse(ctx context.Context, w http.ResponseWriter, r *http.Request) (Principal, error) {
	if r.PostFormValue("SAMLResponse") == "" {
		return Principal{}, errors.New("saml: missing SAMLResponse form field")
	}
	tracked, err := s.tracker.GetTrackedRequest(r, r.PostFormValue("RelayState"))
	if err != nil || tracked.SAMLRequestID == "" {
		return Principal{}, errors.New("saml: missing or invalid browser request state")
	}
	assertion, err := s.sp.ParseResponse(r, []string{tracked.SAMLRequestID})
	if err != nil {
		return Principal{}, fmt.Errorf("saml: response rejected: %w", err)
	}
	ext, err := s.identityFromAssertion(assertion)
	if err != nil {
		return Principal{}, err
	}
	if !s.pending.consume(tracked.SAMLRequestID) {
		return Principal{}, errors.New("saml: request id not pending (possible replay)")
	}
	http.SetCookie(w, &http.Cookie{
		Name: s.tracker.NamePrefix + tracked.Index, Path: s.sp.AcsURL.Path,
		MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteNoneMode,
	})
	principal, err := s.bridge.ResolveExternal(ctx, ext)
	if err != nil {
		return Principal{}, fmt.Errorf("saml: identity resolution: %w", err)
	}
	return principal, nil
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
	return &samlIDStore{ttl: ttl}
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

// consume atomically removes one specific live request after signature and
// browser-state verification. The bounded store needs no background reaper.
func (s *samlIDStore) consume(id string) bool {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	used := false
	kept := s.ids[:0]
	for _, e := range s.ids {
		if e.id == id && now.Before(e.expires) {
			used = true
		}
		if e.id != id && now.Before(e.expires) {
			kept = append(kept, e)
		}
	}
	s.ids = kept
	return used
}
