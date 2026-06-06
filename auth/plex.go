package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	plexPinsURL     = "https://plex.tv/api/v2/pins"
	plexUserURL     = "https://plex.tv/api/v2/user"
	plexResourceURL = "https://plex.tv/api/v2/resources"
	plexAuthAppURL  = "https://app.plex.tv/auth"
	pinCookieName   = "pp_plex_pin"
)

// PlexProvider implements the Plex SSO PIN/OAuth flow.
type PlexProvider struct {
	// ClientID is a stable identifier for this app instance (X-Plex-Client-Identifier).
	ClientID string
	// Product is the human-readable app name shown on plex.tv.
	Product string
	// MachineID is the Plex server machine identifier the user must have access to.
	MachineID string
	// PublicBaseURL is the externally reachable base URL used to build the callback (forwardUrl).
	PublicBaseURL string
	// Secure controls the Secure flag on the temporary PIN cookie.
	Secure bool

	http *http.Client
}

// NewPlexProvider builds a Plex auth provider.
func NewPlexProvider(clientID, product, machineID, publicBaseURL string, secure bool) *PlexProvider {
	return &PlexProvider{
		ClientID:      clientID,
		Product:       product,
		MachineID:     machineID,
		PublicBaseURL: strings.TrimRight(publicBaseURL, "/"),
		Secure:        secure,
		http:          &http.Client{Timeout: 15 * time.Second},
	}
}

type plexPin struct {
	ID   int    `json:"id"`
	Code string `json:"code"`
}

func (p *PlexProvider) plexHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Product", p.Product)
	req.Header.Set("X-Plex-Client-Identifier", p.ClientID)
}

// StartLogin requests a PIN from plex.tv and returns the plex.tv auth URL.
func (p *PlexProvider) StartLogin(w http.ResponseWriter, r *http.Request) (string, error) {
	form := url.Values{"strong": {"true"}}
	req, err := http.NewRequest(http.MethodPost, plexPinsURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	p.plexHeaders(req)

	resp, err := p.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("request plex pin: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("plex pin request failed: status %d", resp.StatusCode)
	}

	var pin plexPin
	if err := json.NewDecoder(resp.Body).Decode(&pin); err != nil {
		return "", fmt.Errorf("decode plex pin: %w", err)
	}

	// Remember the pin id across the redirect via a short-lived cookie.
	http.SetCookie(w, &http.Cookie{
		Name:     pinCookieName,
		Value:    strconv.Itoa(pin.ID),
		Path:     "/",
		HttpOnly: true,
		Secure:   p.Secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   600,
	})

	forwardURL := p.PublicBaseURL + "/auth/callback"
	authURL := plexAuthAppURL + "#?" + url.Values{
		"clientID":                     {p.ClientID},
		"code":                         {pin.Code},
		"forwardUrl":                   {forwardURL},
		"context[device][product]":     {p.Product},
	}.Encode()

	return authURL, nil
}

type plexUser struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Title    string `json:"title"`
}

// HandleCallback exchanges the stored PIN for an auth token, then validates the
// user has access to the configured server and whether they own it (admin).
func (p *PlexProvider) HandleCallback(w http.ResponseWriter, r *http.Request) (*User, error) {
	c, err := r.Cookie(pinCookieName)
	if err != nil {
		return nil, errors.New("missing plex pin cookie")
	}
	// Clear the pin cookie regardless of outcome.
	http.SetCookie(w, &http.Cookie{Name: pinCookieName, Value: "", Path: "/", MaxAge: -1})

	pinID := c.Value
	token, err := p.pollToken(pinID)
	if err != nil {
		return nil, err
	}

	u, err := p.fetchUser(token)
	if err != nil {
		return nil, err
	}

	isAdmin, hasAccess, err := p.checkServerAccess(token)
	if err != nil {
		return nil, err
	}
	if !hasAccess && !isAdmin {
		return nil, errors.New("user has no access to this Plex server")
	}

	username := u.Username
	if username == "" {
		username = u.Title
	}
	return &User{Username: username, Email: u.Email, IsAdmin: isAdmin}, nil
}

func (p *PlexProvider) pollToken(pinID string) (string, error) {
	u := fmt.Sprintf("%s/%s", plexPinsURL, pinID)
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	p.plexHeaders(req)

	resp, err := p.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("poll plex pin: %w", err)
	}
	defer resp.Body.Close()

	var out struct {
		AuthToken string `json:"authToken"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode plex pin token: %w", err)
	}
	if out.AuthToken == "" {
		return "", errors.New("plex authentication not completed")
	}
	return out.AuthToken, nil
}

func (p *PlexProvider) fetchUser(token string) (*plexUser, error) {
	req, err := http.NewRequest(http.MethodGet, plexUserURL, nil)
	if err != nil {
		return nil, err
	}
	p.plexHeaders(req)
	req.Header.Set("X-Plex-Token", token)

	resp, err := p.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch plex user: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch plex user failed: status %d", resp.StatusCode)
	}

	var u plexUser
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return nil, fmt.Errorf("decode plex user: %w", err)
	}
	return &u, nil
}

type plexResource struct {
	ClientIdentifier string `json:"clientIdentifier"`
	Owned            bool   `json:"owned"`
	Provides         string `json:"provides"`
}

// checkServerAccess returns (isAdmin, hasAccess) by listing the user's resources
// and finding the configured server by machine id.
func (p *PlexProvider) checkServerAccess(token string) (isAdmin, hasAccess bool, err error) {
	u := plexResourceURL + "?" + url.Values{"includeHttps": {"1"}}.Encode()
	req, reqErr := http.NewRequest(http.MethodGet, u, nil)
	if reqErr != nil {
		return false, false, reqErr
	}
	p.plexHeaders(req)
	req.Header.Set("X-Plex-Token", token)

	resp, doErr := p.http.Do(req)
	if doErr != nil {
		return false, false, fmt.Errorf("fetch plex resources: %w", doErr)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, false, fmt.Errorf("fetch plex resources failed: status %d", resp.StatusCode)
	}

	var resources []plexResource
	if err := json.NewDecoder(resp.Body).Decode(&resources); err != nil {
		return false, false, fmt.Errorf("decode plex resources: %w", err)
	}

	for _, res := range resources {
		if res.ClientIdentifier == p.MachineID {
			return res.Owned, true, nil
		}
	}
	return false, false, nil
}
