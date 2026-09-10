package auth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-ldap/ldap/v3"
	"github.com/google/uuid"
)

var ErrDirectoryCredentials = errors.New("directory authentication failed")

// LDAPConfig is one trusted directory endpoint. ldap:// always uses StartTLS;
// there is deliberately no plaintext bind, referral or certificate bypass mode.
type LDAPConfig struct {
	URL, Issuer, Kind                   string
	BaseDN, BindDN, BindPassword        string
	UsernameAttribute, SubjectAttribute string
	RequiredGroupDN, GroupBaseDN        string
	GroupMemberAttribute                string
	RootCAs                             *x509.CertPool
	Timeout                             time.Duration
}

type LDAPIdentity struct {
	Issuer, Subject, Email string
	IssuedAt               int64 // start of the verified password authentication attempt
}

type LDAPVerifier struct {
	cfg             LDAPConfig
	endpoint        *url.URL
	base, groupBase *ldap.DN
	requiredGroup   *ldap.DN
}

var ldapAttributeName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9-]*$`)

func NewLDAPVerifier(cfg LDAPConfig) (*LDAPVerifier, error) {
	u, err := url.Parse(cfg.URL)
	if err != nil || (u.Scheme != "ldap" && u.Scheme != "ldaps") || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || strings.Contains(cfg.URL, "#") {
		return nil, errors.New("LDAP URL must be one ldap:// or ldaps:// host without credentials, path, query or fragment")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, errors.New("invalid LDAP port")
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return nil, errors.New("empty LDAP port")
	}
	if cfg.Issuer == "" || strings.TrimSpace(cfg.Issuer) != cfg.Issuer {
		return nil, errors.New("LDAP issuer must be an exact nonempty value")
	}
	if cfg.Kind != "ad" && cfg.Kind != "ldap" {
		return nil, errors.New("LDAP directory kind must be ad or ldap")
	}
	base, err := ldap.ParseDN(cfg.BaseDN)
	if err != nil || len(base.RDNs) == 0 {
		return nil, errors.New("LDAP base DN is required")
	}
	bind, err := ldap.ParseDN(cfg.BindDN)
	if err != nil || len(bind.RDNs) == 0 || cfg.BindPassword == "" {
		return nil, errors.New("LDAP service bind DN and password are required")
	}
	if cfg.UsernameAttribute == "" {
		cfg.UsernameAttribute = "uid"
		if cfg.Kind == "ad" {
			cfg.UsernameAttribute = "sAMAccountName"
		}
	}
	if cfg.SubjectAttribute == "" {
		cfg.SubjectAttribute = "entryUUID"
		if cfg.Kind == "ad" {
			cfg.SubjectAttribute = "objectGUID"
		}
	}
	if cfg.GroupMemberAttribute == "" {
		cfg.GroupMemberAttribute = "member"
	}
	for _, attr := range []string{cfg.UsernameAttribute, cfg.SubjectAttribute, cfg.GroupMemberAttribute} {
		if !ldapAttributeName.MatchString(attr) {
			return nil, errors.New("invalid LDAP attribute name")
		}
	}
	if cfg.Kind == "ad" && !strings.EqualFold(cfg.SubjectAttribute, "objectGUID") {
		return nil, errors.New("AD subject attribute must be objectGUID")
	}
	switch strings.ToLower(cfg.SubjectAttribute) {
	case "mail", "email", "uid", "userprincipalname", "samaccountname", "cn", "displayname", "dn", "distinguishedname":
		return nil, errors.New("LDAP subject attribute must be immutable, not email, name or DN")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Second
	}
	if cfg.Timeout < 50*time.Millisecond || cfg.Timeout > 30*time.Second {
		return nil, errors.New("LDAP timeout must be between 50ms and 30s")
	}
	if cfg.GroupBaseDN == "" {
		cfg.GroupBaseDN = cfg.BaseDN
	}
	groupBase, err := ldap.ParseDN(cfg.GroupBaseDN)
	if err != nil || len(groupBase.RDNs) == 0 {
		return nil, errors.New("invalid LDAP group base DN")
	}
	var required *ldap.DN
	if cfg.RequiredGroupDN != "" {
		required, err = ldap.ParseDN(cfg.RequiredGroupDN)
		if err != nil || len(required.RDNs) == 0 || !(groupBase.Equal(required) || groupBase.AncestorOf(required)) {
			return nil, errors.New("required LDAP group must be within group base DN")
		}
	}
	return &LDAPVerifier{cfg: cfg, endpoint: u, base: base, groupBase: groupBase, requiredGroup: required}, nil
}

func (v *LDAPVerifier) connect(ctx context.Context) (*ldap.Conn, func(), error) {
	port := v.endpoint.Port()
	if port == "" {
		port = "636"
		if v.endpoint.Scheme == "ldap" {
			port = "389"
		}
	}
	address := net.JoinHostPort(v.endpoint.Hostname(), port)
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: v.endpoint.Hostname(), RootCAs: v.cfg.RootCAs}
	dialer := &net.Dialer{Timeout: v.cfg.Timeout}
	var raw net.Conn
	var err error
	secure := v.endpoint.Scheme == "ldaps"
	if secure {
		raw, err = (&tls.Dialer{NetDialer: dialer, Config: tlsConfig}).DialContext(ctx, "tcp", address)
	} else {
		raw, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return nil, nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = raw.SetDeadline(deadline)
	}
	conn := ldap.NewConn(raw, secure)
	conn.Start()
	conn.SetTimeout(v.cfg.Timeout)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	cleanup := func() { stop(); _ = conn.Close() }
	if !secure {
		if err := conn.StartTLS(tlsConfig); err != nil {
			cleanup()
			return nil, nil, err
		}
	}
	return conn, cleanup, nil
}

func (v *LDAPVerifier) search(conn *ldap.Conn, base string, scope, limit int, filter string, attrs []string) (*ldap.SearchResult, error) {
	req := ldap.NewSearchRequest(base, scope, ldap.NeverDerefAliases, limit, int((v.cfg.Timeout+time.Second-1)/time.Second), false, filter, attrs, nil)
	req.EnforceSizeLimit = true
	result, err := conn.Search(req)
	if err != nil {
		return nil, err
	}
	if len(result.Referrals) > 0 {
		return nil, ErrDirectoryCredentials
	}
	return result, nil
}

// Authenticate uses a read-only service account to locate one entry and a
// separate connection to verify its password. It never provisions privileges.
func (v *LDAPVerifier) Authenticate(parent context.Context, username, password string) (LDAPIdentity, error) {
	if username == "" || password == "" || len(username) > 1024 || len(password) > 4096 || !utf8.ValidString(username) {
		return LDAPIdentity{}, ErrDirectoryCredentials
	}
	issuedAt := time.Now().Unix()
	ctx, cancel := context.WithTimeout(parent, v.cfg.Timeout)
	defer cancel()
	service, closeService, err := v.connect(ctx)
	if err != nil {
		return LDAPIdentity{}, ErrDirectoryCredentials
	}
	defer closeService()
	if err := service.Bind(v.cfg.BindDN, v.cfg.BindPassword); err != nil {
		return LDAPIdentity{}, ErrDirectoryCredentials
	}
	attrs := []string{v.cfg.SubjectAttribute, "mail", "userAccountControl", "msDS-User-Account-Control-Computed", "pwdAccountLockedTime"}
	result, err := v.search(service, v.cfg.BaseDN, ldap.ScopeWholeSubtree, 2, "("+v.cfg.UsernameAttribute+"="+ldap.EscapeFilter(username)+")", attrs)
	if err != nil || len(result.Entries) != 1 {
		return LDAPIdentity{}, ErrDirectoryCredentials
	}
	entry := result.Entries[0]
	dn, err := ldap.ParseDN(entry.DN)
	if err != nil || !(v.base.Equal(dn) || v.base.AncestorOf(dn)) {
		return LDAPIdentity{}, ErrDirectoryCredentials
	}
	subject, err := v.subject(entry)
	if err != nil {
		return LDAPIdentity{}, ErrDirectoryCredentials
	}
	if !v.accountEnabled(entry) {
		return LDAPIdentity{}, ErrDirectoryCredentials
	}
	user, closeUser, err := v.connect(ctx)
	if err != nil {
		return LDAPIdentity{}, ErrDirectoryCredentials
	}
	defer closeUser()
	if err := user.Bind(entry.DN, password); err != nil {
		return LDAPIdentity{}, ErrDirectoryCredentials
	}
	// A DN can be reused after deletion. Do not issue the identity found before
	// the bind if the password authenticated a replacement entry at that DN.
	current, err := v.search(service, entry.DN, ldap.ScopeBaseObject, 2, "("+v.cfg.SubjectAttribute+"=*)", attrs)
	if err != nil || len(current.Entries) != 1 {
		return LDAPIdentity{}, ErrDirectoryCredentials
	}
	currentDN, err := ldap.ParseDN(current.Entries[0].DN)
	if err != nil || !dn.Equal(currentDN) {
		return LDAPIdentity{}, ErrDirectoryCredentials
	}
	currentSubject, err := v.subject(current.Entries[0])
	if err != nil || currentSubject != subject || !v.accountEnabled(current.Entries[0]) {
		return LDAPIdentity{}, ErrDirectoryCredentials
	}
	if v.requiredGroup != nil {
		if ok, err := v.inRequiredGroup(service, entry.DN); err != nil || !ok {
			return LDAPIdentity{}, ErrDirectoryCredentials
		}
	}
	if ctx.Err() != nil {
		return LDAPIdentity{}, ErrDirectoryCredentials
	}
	return LDAPIdentity{Issuer: v.cfg.Issuer, Subject: subject, Email: entry.GetEqualFoldAttributeValue("mail"), IssuedAt: issuedAt}, nil
}

func (v *LDAPVerifier) accountEnabled(entry *ldap.Entry) bool {
	if v.cfg.Kind != "ad" {
		return len(entry.GetEqualFoldAttributeValues("pwdAccountLockedTime")) == 0
	}
	for attr, mask := range map[string]uint64{"userAccountControl": 2, "msDS-User-Account-Control-Computed": 16 | 0x800000} {
		values := entry.GetEqualFoldAttributeValues(attr)
		if len(values) > 1 {
			return false
		}
		if len(values) == 1 {
			n, err := strconv.ParseUint(values[0], 10, 64)
			if err != nil || n&mask != 0 {
				return false
			}
		}
	}
	return true
}

func (v *LDAPVerifier) subject(entry *ldap.Entry) (string, error) {
	values := entry.GetEqualFoldRawAttributeValues(v.cfg.SubjectAttribute)
	if len(values) != 1 || len(values[0]) == 0 {
		return "", ErrDirectoryCredentials
	}
	raw := values[0]
	if v.cfg.Kind == "ad" {
		if len(raw) != 16 {
			return "", ErrDirectoryCredentials
		}
		ordered := append([]byte(nil), raw...)
		ordered[0], ordered[1], ordered[2], ordered[3] = raw[3], raw[2], raw[1], raw[0]
		ordered[4], ordered[5], ordered[6], ordered[7] = raw[5], raw[4], raw[7], raw[6]
		id, err := uuid.FromBytes(ordered)
		if err != nil || id == uuid.Nil {
			return "", ErrDirectoryCredentials
		}
		return id.String(), nil
	}
	if !utf8.Valid(raw) || len(raw) > 1024 {
		return "", ErrDirectoryCredentials
	}
	if strings.EqualFold(v.cfg.SubjectAttribute, "entryUUID") {
		id, err := uuid.Parse(string(raw))
		if err != nil || id == uuid.Nil {
			return "", ErrDirectoryCredentials
		}
		return id.String(), nil
	}
	return string(raw), nil // explicit immutable text attribute; never email/DN fallback
}

const ldapGroupDepthLimit = 8
const ldapGroupNodeLimit = 256

func (v *LDAPVerifier) inRequiredGroup(service *ldap.Conn, userDN string) (bool, error) {
	if v.cfg.Kind == "ad" {
		result, err := v.search(service, userDN, ldap.ScopeBaseObject, 2, "(memberOf:1.2.840.113556.1.4.1941:="+ldap.EscapeFilter(v.cfg.RequiredGroupDN)+")", []string{"1.1"})
		if err != nil || len(result.Entries) != 1 {
			return false, err
		}
		dn, err := ldap.ParseDN(result.Entries[0].DN)
		expected, parseErr := ldap.ParseDN(userDN)
		return err == nil && parseErr == nil && dn.Equal(expected), err
	}
	frontier := []string{userDN}
	visited := []*ldap.DN{}
	for depth := 0; len(frontier) > 0; depth++ {
		if depth >= ldapGroupDepthLimit {
			return false, ErrDirectoryCredentials
		}
		next := []string{}
		for _, memberDN := range frontier {
			result, err := v.search(service, v.cfg.GroupBaseDN, ldap.ScopeWholeSubtree, ldapGroupNodeLimit+1, "("+v.cfg.GroupMemberAttribute+"="+ldap.EscapeFilter(memberDN)+")", []string{"1.1"})
			if err != nil {
				return false, err
			}
			if len(result.Entries)+len(visited) > ldapGroupNodeLimit {
				return false, ErrDirectoryCredentials
			}
			for _, entry := range result.Entries {
				dn, err := ldap.ParseDN(entry.DN)
				if err != nil || !(v.groupBase.Equal(dn) || v.groupBase.AncestorOf(dn)) {
					return false, ErrDirectoryCredentials
				}
				if dn.Equal(v.requiredGroup) {
					return true, nil
				}
				seen := false
				for _, old := range visited {
					if old.Equal(dn) {
						seen = true
						break
					}
				}
				if !seen {
					visited = append(visited, dn)
					next = append(next, entry.DN)
				}
			}
		}
		frontier = next
	}
	return false, nil
}

func (v *LDAPVerifier) String() string { return "LDAPVerifier(" + v.cfg.Issuer + ")" }
