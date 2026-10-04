// Package sso implements P3 single sign-on: OIDC (Google, Microsoft Entra)
// with the standard library only — discovery, code exchange, JWKS RS256
// verification. SAML stays an enterprise add-on (interface note below).
//
//   SSO_GOOGLE_CLIENT_ID / SSO_GOOGLE_CLIENT_SECRET / SSO_GOOGLE_REDIRECT_URL
//   SSO_MICROSOFT_CLIENT_ID / SSO_MICROSOFT_CLIENT_SECRET / SSO_MICROSOFT_REDIRECT_URL
//   SSO_AUTOPROVISION=1   → unknown verified emails get a fresh tenant+owner
//                         (default 0: SSO is login-only for existing users)
package sso

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Identity is a verified OIDC subject.
type Identity struct {
	Provider      string
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
}

// Provider wires one OIDC issuer.
type Provider struct {
	Slug         string
	IssuerPrefix string // verified with HasPrefix (Entra varies by tenant)
	Discovery    string
	ClientID     string
	Secret       string
	RedirectURL  string
	AuthURL      string
	TokenURL     string
}

func env(k string) string { return strings.TrimSpace(os.Getenv(k)) }

// Google returns the Google provider, or nil when unconfigured.
func Google() *Provider {
	id, secret, redirect := env("SSO_GOOGLE_CLIENT_ID"), env("SSO_GOOGLE_CLIENT_SECRET"), env("SSO_GOOGLE_REDIRECT_URL")
	if id == "" || secret == "" || redirect == "" {
		return nil
	}
	return &Provider{
		Slug: "google", IssuerPrefix: "https://accounts.google.com",
		Discovery:   "https://accounts.google.com/.well-known/openid-configuration",
		ClientID:    id, Secret: secret, RedirectURL: redirect,
		AuthURL:     "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL:    "https://oauth2.googleapis.com/token",
	}
}

// Microsoft returns the Entra (common) provider, or nil when unconfigured.
func Microsoft() *Provider {
	id, secret, redirect := env("SSO_MICROSOFT_CLIENT_ID"), env("SSO_MICROSOFT_CLIENT_SECRET"), env("SSO_MICROSOFT_REDIRECT_URL")
	if id == "" || secret == "" || redirect == "" {
		return nil
	}
	return &Provider{
		Slug: "microsoft", IssuerPrefix: "https://login.microsoftonline.com/",
		Discovery:   "https://login.microsoftonline.com/common/v2.0/.well-known/openid-configuration",
		ClientID:    id, Secret: secret, RedirectURL: redirect,
		AuthURL:     "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
		TokenURL:    "https://login.microsoftonline.com/common/oauth2/v2.0/token",
	}
}

// Get returns the configured provider by slug.
func Get(slug string) *Provider {
	switch strings.ToLower(slug) {
	case "google":
		return Google()
	case "microsoft":
		return Microsoft()
	default:
		return nil
	}
}

// Status reports which providers are live (no secrets).
func Status() map[string]bool {
	return map[string]bool{"google": Google() != nil, "microsoft": Microsoft() != nil}
}

// Autoprovision reports SSO_AUTOPROVISION=1.
func Autoprovision() bool { return env("SSO_AUTOPROVISION") == "1" }

// StartURL builds the authorization redirect.
func (p *Provider) StartURL(state string) string {
	q := url.Values{
		"client_id": {p.ClientID}, "redirect_uri": {p.RedirectURL},
		"response_type": {"code"}, "scope": {"openid email profile"},
		"state": {state},
	}
	return p.AuthURL + "?" + q.Encode()
}

// jwksCache caches keys per discovery URL for 1h.
var jwksCache = struct {
	sync.Mutex
	keys map[string]cachedKeys
}{keys: map[string]cachedKeys{}}

type cachedKeys struct {
	keys map[string]*rsa.PublicKey
	exp  time.Time
}

type jwksDoc struct {
	Keys []struct {
		Kid string `json:"kid"`
		N   string `json:"n"`
		E   string `json:"e"`
	} `json:"keys"`
}

func (p *Provider) keys(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	jwksCache.Lock()
	if c, ok := jwksCache.keys[p.Discovery]; ok && time.Now().Before(c.exp) {
		jwksCache.Unlock()
		return c.keys, nil
	}
	jwksCache.Unlock()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.Discovery, nil)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var disc struct {
		JwksURI string `json:"jwks_uri"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&disc); err != nil || disc.JwksURI == "" {
		return nil, fmt.Errorf("sso: bad discovery")
	}
	req2, _ := http.NewRequestWithContext(ctx, http.MethodGet, disc.JwksURI, nil)
	resp2, err := client.Do(req2)
	if err != nil {
		return nil, err
	}
	defer resp2.Body.Close()
	var doc jwksDoc
	if err := json.NewDecoder(resp2.Body).Decode(&doc); err != nil {
		return nil, err
	}
	out := map[string]*rsa.PublicKey{}
	for _, k := range doc.Keys {
		nb, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			continue
		}
		eb, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			continue
		}
		n := new(big.Int).SetBytes(nb)
		exp := 0
		for _, b := range eb {
			exp = exp<<8 + int(b)
		}
		out[k.Kid] = &rsa.PublicKey{N: n, E: exp}
	}
	jwksCache.Lock()
	jwksCache.keys[p.Discovery] = cachedKeys{keys: out, exp: time.Now().Add(time.Hour)}
	jwksCache.Unlock()
	return out, nil
}

// Exchange swaps code for a verified identity.
func (p *Provider) Exchange(ctx context.Context, code string) (*Identity, error) {
	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {code},
		"redirect_uri": {p.RedirectURL},
		"client_id":    {p.ClientID}, "client_secret": {p.Secret},
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.TokenURL,
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var tok struct {
		IDToken string `json:"id_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil || tok.IDToken == "" {
		return nil, fmt.Errorf("sso: bad token response")
	}
	return p.verify(ctx, tok.IDToken)
}

func (p *Provider) verify(ctx context.Context, raw string) (*Identity, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("sso: malformed jwt")
	}
	var header struct {
		Kid string `json:"kid"`
		Alg string `json:"alg"`
	}
	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(hb, &header) != nil {
		return nil, fmt.Errorf("sso: bad jwt header")
	}
	if header.Alg != "RS256" || header.Kid == "" {
		return nil, fmt.Errorf("sso: unsupported jwt alg")
	}
	keys, err := p.keys(ctx)
	if err != nil {
		return nil, err
	}
	pub, ok := keys[header.Kid]
	if !ok {
		return nil, fmt.Errorf("sso: unknown key")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("sso: bad signature")
	}
	h := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, h[:], sig); err != nil {
		return nil, fmt.Errorf("sso: bad signature")
	}
	pb, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("sso: bad claims")
	}
	var claims struct {
		Iss           string `json:"iss"`
		Aud           any    `json:"aud"`
		Exp           int64  `json:"exp"`
		Sub           string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified any    `json:"email_verified"`
		Name          string `json:"name"`
	}
	if err := json.Unmarshal(pb, &claims); err != nil {
		return nil, fmt.Errorf("sso: bad claims")
	}
	if !strings.HasPrefix(claims.Iss, p.IssuerPrefix) && claims.Iss != p.IssuerPrefix {
		return nil, fmt.Errorf("sso: bad issuer")
	}
	audOK := false
	switch a := claims.Aud.(type) {
	case string:
		audOK = a == p.ClientID
	case []any:
		for _, v := range a {
			if s, _ := v.(string); s == p.ClientID {
				audOK = true
			}
		}
	}
	if !audOK {
		return nil, fmt.Errorf("sso: bad audience")
	}
	if time.Until(time.Unix(claims.Exp, 0)) <= 0 {
		return nil, fmt.Errorf("sso: expired")
	}
	if claims.Sub == "" {
		return nil, fmt.Errorf("sso: no subject")
	}
	verified := false
	switch v := claims.EmailVerified.(type) {
	case bool:
		verified = v
	case string:
		verified = v == "true"
	}
	return &Identity{
		Provider: p.Slug, Subject: claims.Sub,
		Email: strings.ToLower(strings.TrimSpace(claims.Email)),
		EmailVerified: verified, Name: claims.Name,
	}, nil
}
