package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ber "github.com/go-asn1-ber/asn1-ber"
	"github.com/go-ldap/ldap/v3"
)

// ldapWire is a local BER-speaking TLS fixture, not a mocked Authenticate call.
type ldapWire struct {
	listener                        net.Listener
	tls                             *tls.Config
	config                          LDAPConfig
	startTLS                        bool
	refuseTLS                       bool
	expired                         bool
	wrongHost                       bool
	referral                        bool
	ambiguous                       bool
	stall                           bool
	changeIdentityOnBind            bool
	identityChanged                 atomic.Bool
	username, userDN                string
	attrs                           map[string][]string
	groups                          map[string][]string
	adMember                        bool
	mu                              sync.Mutex
	connections                     map[net.Conn]bool
	binds, searches, plaintextBinds int
	filters                         []string
	closed                          chan struct{}
	searchStarted                   chan struct{}
	wg                              sync.WaitGroup
}

func newLDAPWire(t *testing.T, configure func(*ldapWire)) *ldapWire {
	t.Helper()
	f := &ldapWire{username: "alice", userDN: "uid=alice,dc=test", attrs: map[string][]string{"entryUUID": {"00112233-4455-6677-8899-aabbccddeeff"}, "mail": {"claim@example.test"}}, groups: map[string][]string{}, connections: map[net.Conn]bool{}, closed: make(chan struct{}), searchStarted: make(chan struct{}, 1)}
	if configure != nil {
		configure(f)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "local test directory"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	if f.expired {
		template.NotAfter = time.Now().Add(-time.Minute)
	}
	if f.wrongHost {
		template.IPAddresses = []net.IP{net.ParseIP("192.0.2.1")}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	f.tls = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
	f.listener, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	scheme := "ldaps"
	if f.startTLS {
		scheme = "ldap"
	}
	f.config = LDAPConfig{URL: scheme + "://" + f.listener.Addr().String(), Issuer: "directory-issuer", Kind: "ldap", BaseDN: "dc=test", BindDN: "cn=reader,dc=test", BindPassword: "service-secret", RootCAs: pool, Timeout: time.Second}
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		for {
			c, err := f.listener.Accept()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.connections[c] = true
			f.mu.Unlock()
			f.wg.Add(1)
			go func() {
				defer f.wg.Done()
				defer c.Close()
				defer func() { f.mu.Lock(); delete(f.connections, c); f.mu.Unlock() }()
				f.serve(c)
			}()
		}
	}()
	t.Cleanup(func() {
		close(f.closed)
		_ = f.listener.Close()
		f.mu.Lock()
		for c := range f.connections {
			_ = c.Close()
		}
		f.mu.Unlock()
		f.wg.Wait()
	})
	return f
}

func ldapString(value string) *ber.Packet {
	return ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, value, "")
}
func ldapResult(tag ber.Tag, code int) *ber.Packet {
	p := ber.Encode(ber.ClassApplication, ber.TypeConstructed, tag, nil, "")
	p.AppendChild(ber.NewInteger(ber.ClassUniversal, ber.TypePrimitive, ber.TagEnumerated, code, ""))
	p.AppendChild(ldapString(""))
	p.AppendChild(ldapString(""))
	return p
}
func ldapReply(c net.Conn, id int64, p *ber.Packet) error {
	m := ber.NewSequence("")
	m.AppendChild(ber.NewInteger(ber.ClassUniversal, ber.TypePrimitive, ber.TagInteger, id, ""))
	m.AppendChild(p)
	_, err := c.Write(m.Bytes())
	return err
}
func ldapEntry(dn string, attrs map[string][]string) *ber.Packet {
	p := ber.Encode(ber.ClassApplication, ber.TypeConstructed, 4, nil, "")
	p.AppendChild(ldapString(dn))
	list := ber.NewSequence("")
	for name, values := range attrs {
		attr := ber.NewSequence("")
		attr.AppendChild(ldapString(name))
		set := ber.Encode(ber.ClassUniversal, ber.TypeConstructed, ber.TagSet, nil, "")
		for _, v := range values {
			set.AppendChild(ldapString(v))
		}
		attr.AppendChild(set)
		list.AppendChild(attr)
	}
	p.AppendChild(list)
	return p
}

func (f *ldapWire) serve(raw net.Conn) {
	c := raw
	secure := !f.startTLS
	if secure {
		tlsConn := tls.Server(c, f.tls)
		if tlsConn.Handshake() != nil {
			return
		}
		c = tlsConn
	}
	bound := ""
	for {
		message, err := ber.ReadPacket(c)
		if err != nil || len(message.Children) != 2 {
			return
		}
		id, ok := message.Children[0].Value.(int64)
		if !ok {
			return
		}
		op := message.Children[1]
		switch op.Tag {
		case 23:
			if f.refuseTLS {
				_ = ldapReply(c, id, ldapResult(24, 53))
				return
			}
			if len(op.Children) != 1 || string(op.Children[0].Data.Bytes()) != "1.3.6.1.4.1.1466.20037" {
				return
			}
			if ldapReply(c, id, ldapResult(24, 0)) != nil {
				return
			}
			tlsConn := tls.Server(c, f.tls)
			if tlsConn.Handshake() != nil {
				return
			}
			c = tlsConn
			secure = true
		case 0:
			if len(op.Children) != 3 {
				return
			}
			f.mu.Lock()
			f.binds++
			if !secure {
				f.plaintextBinds++
			}
			f.mu.Unlock()
			dn := string(op.Children[1].Data.Bytes())
			password := string(op.Children[2].Data.Bytes())
			code := 49
			if secure && ((dn == f.config.BindDN && password == f.config.BindPassword) || (dn == f.userDN && password == "user-secret")) {
				code = 0
				bound = dn
				if dn == f.userDN && f.changeIdentityOnBind {
					f.identityChanged.Store(true)
				}
			}
			if ldapReply(c, id, ldapResult(1, code)) != nil {
				return
			}
		case 3:
			if bound != f.config.BindDN || len(op.Children) != 8 {
				return
			}
			filter, err := ldap.DecompileFilter(op.Children[6])
			if err != nil {
				return
			}
			f.mu.Lock()
			f.searches++
			f.filters = append(f.filters, filter)
			f.mu.Unlock()
			select {
			case f.searchStarted <- struct{}{}:
			default:
			}
			if f.stall {
				<-f.closed
				return
			}
			if f.referral {
				p := ber.Encode(ber.ClassApplication, ber.TypeConstructed, 19, nil, "")
				p.AppendChild(ldapString("ldap://127.0.0.1:1/dc=other"))
				_ = ldapReply(c, id, p)
			} else {
				usernameAttr := "uid"
				if f.config.Kind == "ad" {
					usernameAttr = "sAMAccountName"
				}
				switch {
				case filter == "("+usernameAttr+"="+ldap.EscapeFilter(f.username)+")":
					_ = ldapReply(c, id, ldapEntry(f.userDN, f.attrs))
					if f.ambiguous {
						_ = ldapReply(c, id, ldapEntry("uid=other,dc=test", f.attrs))
					}
				case filter == "(entryUUID=*)" || filter == "(objectGUID=*)":
					attrs := f.attrs
					if f.identityChanged.Load() {
						attrs = map[string][]string{"entryUUID": {"11112233-4455-6677-8899-aabbccddeeff"}}
					}
					_ = ldapReply(c, id, ldapEntry(f.userDN, attrs))
				case strings.HasPrefix(filter, "(memberOf:1.2.840.113556.1.4.1941:="):
					if f.adMember {
						_ = ldapReply(c, id, ldapEntry(f.userDN, nil))
					}
				default:
					for member, groups := range f.groups {
						if filter == "(member="+ldap.EscapeFilter(member)+")" {
							for _, dn := range groups {
								_ = ldapReply(c, id, ldapEntry(dn, nil))
							}
						}
					}
				}
			}
			if ldapReply(c, id, ldapResult(5, 0)) != nil {
				return
			}
		case 2:
			return
		default:
			return
		}
	}
}

func TestLDAPTLSBindAndStableIdentity(t *testing.T) {
	for _, startTLS := range []bool{false, true} {
		t.Run(fmt.Sprint(startTLS), func(t *testing.T) {
			f := newLDAPWire(t, func(f *ldapWire) { f.startTLS = startTLS; f.username = "literal*)(uid=*)" })
			v, err := NewLDAPVerifier(f.config)
			if err != nil {
				t.Fatal(err)
			}
			p, err := v.Authenticate(context.Background(), f.username, "user-secret")
			if err != nil || p.Subject != "00112233-4455-6677-8899-aabbccddeeff" || p.Issuer != "directory-issuer" || p.IssuedAt == 0 {
				t.Fatalf("identity=%+v err=%v", p, err)
			}
			f.mu.Lock()
			binds, plain := f.binds, f.plaintextBinds
			filters := append([]string(nil), f.filters...)
			f.mu.Unlock()
			if binds != 2 || plain != 0 || len(filters) != 2 || filters[0] != "(uid=literal\\2a\\29\\28uid=\\2a\\29)" || filters[1] != "(entryUUID=*)" {
				t.Fatalf("binds=%d plain=%d filters=%v", binds, plain, filters)
			}
		})
	}
}

func TestLDAPRejectsUnsafeOrFailedAuthentication(t *testing.T) {
	for _, name := range []string{"untrusted certificate", "expired certificate", "hostname", "StartTLS refused", "empty username", "empty password", "wrong password", "wrong service password", "ambiguous", "referral", "missing subject", "malformed subject", "locked", "outside base", "timeout"} {
		t.Run(name, func(t *testing.T) {
			f := newLDAPWire(t, func(f *ldapWire) {
				switch name {
				case "expired certificate":
					f.expired = true
				case "hostname":
					f.wrongHost = true
				case "StartTLS refused":
					f.startTLS = true
					f.refuseTLS = true
				case "ambiguous":
					f.ambiguous = true
				case "referral":
					f.referral = true
				case "missing subject":
					delete(f.attrs, "entryUUID")
				case "malformed subject":
					f.attrs["entryUUID"] = []string{"not-a-uuid"}
				case "locked":
					f.attrs["pwdAccountLockedTime"] = []string{"000001010000Z"}
				case "outside base":
					f.userDN = "uid=alice,dc=foreign"
				case "timeout":
					f.stall = true
				}
			})
			cfg := f.config
			username, password := f.username, "user-secret"
			switch name {
			case "untrusted certificate":
				cfg.RootCAs = x509.NewCertPool()
			case "empty username":
				username = ""
			case "empty password":
				password = ""
			case "wrong password":
				password = "bad"
			case "wrong service password":
				cfg.BindPassword = "bad"
			case "timeout":
				cfg.Timeout = 70 * time.Millisecond
			}
			v, err := NewLDAPVerifier(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if p, err := v.Authenticate(context.Background(), username, password); err == nil || p.Subject != "" {
				t.Fatalf("accepted %+v err=%v", p, err)
			}
			f.mu.Lock()
			plain, binds := f.plaintextBinds, f.binds
			f.mu.Unlock()
			if plain != 0 {
				t.Fatal("credentials sent before TLS")
			}
			if (strings.Contains(name, "certificate") || name == "hostname" || name == "StartTLS refused" || strings.HasPrefix(name, "empty")) && binds != 0 {
				t.Fatalf("unexpected credential bind=%d", binds)
			}
		})
	}
}

func TestLDAPADGUIDAndAccountState(t *testing.T) {
	for _, state := range []string{"active", "disabled", "locked", "expired password"} {
		t.Run(state, func(t *testing.T) {
			raw, _ := hex.DecodeString("33221100554477668899aabbccddeeff")
			f := newLDAPWire(t, func(f *ldapWire) {
				f.attrs = map[string][]string{"objectGUID": {string(raw)}, "userAccountControl": {"512"}, "msDS-User-Account-Control-Computed": {"0"}}
				f.adMember = true
				switch state {
				case "disabled":
					f.attrs["userAccountControl"] = []string{"514"}
				case "locked":
					f.attrs["msDS-User-Account-Control-Computed"] = []string{"16"}
				case "expired password":
					f.attrs["msDS-User-Account-Control-Computed"] = []string{"8388608"}
				}
			})
			f.config.Kind = "ad"
			cfg := f.config
			cfg.RequiredGroupDN = "cn=allowed,dc=test"
			v, err := NewLDAPVerifier(cfg)
			if err != nil {
				t.Fatal(err)
			}
			p, err := v.Authenticate(context.Background(), f.username, "user-secret")
			if state == "active" {
				if err != nil || p.Subject != "00112233-4455-6677-8899-aabbccddeeff" {
					t.Fatalf("GUID=%q err=%v", p.Subject, err)
				}
			} else if err == nil {
				t.Fatal("inactive AD account accepted")
			}
		})
	}
}

func TestLDAPNestedGroupsAndLimits(t *testing.T) {
	for _, mode := range []string{"nested", "cycle", "depth", "nodes", "foreign group"} {
		t.Run(mode, func(t *testing.T) {
			f := newLDAPWire(t, func(f *ldapWire) {
				switch mode {
				case "nested":
					f.groups[f.userDN] = []string{"cn=child,dc=test"}
					f.groups["cn=child,dc=test"] = []string{"cn=allowed,dc=test"}
				case "cycle":
					f.groups[f.userDN] = []string{"cn=child,dc=test"}
					f.groups["cn=child,dc=test"] = []string{"cn=parent,dc=test"}
					f.groups["cn=parent,dc=test"] = []string{"cn=child,dc=test"}
				case "depth":
					member := f.userDN
					for i := range ldapGroupDepthLimit + 1 {
						dn := fmt.Sprintf("cn=depth%d,dc=test", i)
						f.groups[member] = []string{dn}
						member = dn
					}
					f.groups[member] = []string{"cn=allowed,dc=test"}
				case "nodes":
					for i := range ldapGroupNodeLimit + 1 {
						f.groups[f.userDN] = append(f.groups[f.userDN], fmt.Sprintf("cn=node%d,dc=test", i))
					}
				case "foreign group":
					f.groups[f.userDN] = []string{"cn=foreign,dc=other"}
				}
			})
			cfg := f.config
			cfg.RequiredGroupDN = "cn=allowed,dc=test"
			v, err := NewLDAPVerifier(cfg)
			if err != nil {
				t.Fatal(err)
			}
			_, err = v.Authenticate(context.Background(), f.username, "user-secret")
			if (mode == "nested") != (err == nil) {
				t.Fatalf("mode=%s err=%v", mode, err)
			}
		})
	}
}

func TestLDAPCancellationAndConcurrentBinds(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		f := newLDAPWire(t, func(f *ldapWire) { f.stall = true })
		v, _ := NewLDAPVerifier(f.config)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { _, err := v.Authenticate(ctx, f.username, "user-secret"); done <- err }()
		select {
		case <-f.searchStarted:
		case <-time.After(time.Second):
			t.Fatal("directory search did not start")
		}
		cancel()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("cancel accepted")
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatal("cancel ignored")
		}
	})
	t.Run("concurrent", func(t *testing.T) {
		f := newLDAPWire(t, nil)
		v, _ := NewLDAPVerifier(f.config)
		var wg sync.WaitGroup
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := v.Authenticate(context.Background(), f.username, "user-secret"); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.binds != 16 || f.plaintextBinds != 0 {
			t.Fatalf("binds=%d plain=%d", f.binds, f.plaintextBinds)
		}
	})
}

func TestLDAPRejectsIdentityReplacementDuringBind(t *testing.T) {
	f := newLDAPWire(t, func(f *ldapWire) { f.changeIdentityOnBind = true })
	v, err := NewLDAPVerifier(f.config)
	if err != nil {
		t.Fatal(err)
	}
	if p, err := v.Authenticate(context.Background(), f.username, "user-secret"); err == nil || p.Subject != "" {
		t.Fatalf("bind to replacement entry reused previous identity: %+v err=%v", p, err)
	}
}
