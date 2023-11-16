package tp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/superfly/macaroon"
	"github.com/superfly/macaroon/internal/merr"
)

type ClientOption func(*Client)

// WithHTTP specifies the HTTP client to use for requests to third parties.
// Third parties may try to set cookies to expedite future discharge flows. This
// may be facilitated by setting the http.Client's Jar field. With cookies
// enabled it's important to use a different cookie jar and hence client when
// fetching discharge tokens for multiple users.
func WithHTTP(h *http.Client) ClientOption {
	return func(c *Client) {
		if c.http == nil {
			c.http = h
			return
		}

		if authed, isAuthed := c.http.Transport.(*authenticatedHTTP); isAuthed {
			authed.t = h.Transport
			cpy := *h
			cpy.Transport = authed
			c.http = &cpy
			return
		}

		c.http = h
	}
}

// WithBearerAuthentication specifies a token to be sent in requests to the
// specified host in the `Authorization: Bearer` header.
func WithBearerAuthentication(hostname, token string) ClientOption {
	return WithAuthentication(hostname, "Bearer "+token)
}

// WithBearerAuthentication specifies a token to be sent in requests to the
// specified host in the `Authorization` header.
func WithAuthentication(hostname, token string) ClientOption {
	return func(c *Client) {
		if c.http == nil {
			cpy := *http.DefaultClient
			c.http = &cpy
		}

		switch t := c.http.Transport.(type) {
		case *authenticatedHTTP:
			t.auth[hostname] = token
		default:
			c.http.Transport = &authenticatedHTTP{
				t:    t,
				auth: map[string]string{hostname: token},
			}
		}
	}
}

// WithUserURLCallback specifies a function to call when when the third party
// needs to interact with the end-user directly. The provided URL should be
// opened in the user's browser if possible. Otherwise it should be displayed to
// the user and they should be instructed to open it themselves. (Optional, but
// attempts at user-interactive discharge flow will fail)
func WithUserURLCallback(cb func(ctx context.Context, url string) error) ClientOption {
	return func(c *Client) {
		c.UserURLCallback = cb
	}
}

// WithPollingBackoff specifies a function determining how long to wait before
// making the next request when polling the third party to see if a discharge is
// ready. This is called the first time with a zero duration. (Optional)
func WithPollingBackoff(nextBackoff func(lastBO time.Duration) (nextBO time.Duration)) ClientOption {
	return func(c *Client) {
		c.PollBackoffNext = nextBackoff
	}
}

type Client struct {
	firstPartyLocation string
	http               *http.Client
	UserURLCallback    func(ctx context.Context, url string) error
	PollBackoffNext    func(lastBO time.Duration) (nextBO time.Duration)
}

// NewClient returns a Client for discharging third party caveats in macaroons
// issued by the specified first party.
func NewClient(firstPartyLocation string, opts ...ClientOption) *Client {
	client := &Client{
		firstPartyLocation: firstPartyLocation,
	}

	for _, opt := range opts {
		opt(client)
	}

	if client.http == nil {
		client.http = http.DefaultClient
	}

	if client.PollBackoffNext == nil {
		client.PollBackoffNext = defaultBackoff
	}

	return client
}

func (c *Client) NeedsDischarge(tokenHeader string) (bool, error) {
	tickets, err := c.undischargedTickets(tokenHeader)
	if err != nil {
		return false, err
	}

	return len(tickets) != 0, nil
}

func (c *Client) FetchDischargeTokens(ctx context.Context, tokenHeader string) (string, error) {
	tickets, err := c.undischargedTickets(tokenHeader)
	if err != nil {
		return "", err
	}

	var (
		wg          sync.WaitGroup
		m           sync.Mutex
		combinedErr error
	)

	for tpLoc, locTickets := range tickets {
		for _, ticket := range locTickets {
			wg.Add(1)
			go func(tpLoc string, ticket []byte) {
				defer wg.Done()

				dis, err := c.fetchDischargeToken(ctx, tpLoc, ticket)

				m.Lock()
				defer m.Unlock()

				if err != nil {
					combinedErr = merr.Append(combinedErr, err)
				} else {
					tokenHeader = tokenHeader + "," + dis
				}
			}(tpLoc, ticket)
		}
	}

	wg.Wait()

	return tokenHeader, combinedErr
}

func (c *Client) undischargedTickets(tokenHeader string) (map[string][][]byte, error) {
	toks, err := macaroon.Parse(tokenHeader)
	if err != nil {
		return nil, err
	}

	perms, _, _, disToks, err := macaroon.FindPermissionAndDischargeTokens(toks, c.firstPartyLocation)
	if err != nil {
		return nil, err
	}

	ret := make(map[string][][]byte)
	for _, perm := range perms {
		tickets, err := perm.ThirdPartyTickets(disToks...)
		if err != nil {
			return nil, err
		}

		for loc, ticket := range tickets {
			ret[loc] = append(ret[loc], ticket)
		}
	}

	return ret, nil
}

func (c *Client) fetchDischargeToken(ctx context.Context, thirdPartyLocation string, ticket []byte) (string, error) {
	jresp, err := c.doInitRequest(ctx, thirdPartyLocation, ticket)

	switch {
	case err != nil:
		return "", err
	case jresp.Discharge != "":
		return jresp.Discharge, nil
	case jresp.PollURL != "":
		return c.doPoll(ctx, jresp.PollURL)
	case jresp.UserInteractive != nil:
		return c.doUserInteractive(ctx, jresp.UserInteractive)
	default:
		return "", errors.New("bad discharge response")
	}
}

func (c *Client) doInitRequest(ctx context.Context, thirdPartyLocation string, ticket []byte) (*jsonResponse, error) {
	jreq := &jsonInitRequest{
		Ticket: ticket,
	}

	breq, err := json.Marshal(jreq)
	if err != nil {
		return nil, err
	}

	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, initURL(thirdPartyLocation), bytes.NewReader(breq))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")

	hresp, err := c.http.Do(hreq)
	if err != nil {
		return nil, err
	}

	var jresp jsonResponse
	if err := json.NewDecoder(hresp.Body).Decode(&jresp); err != nil {
		return nil, fmt.Errorf("bad response (%d): %w", hresp.StatusCode, err)
	}

	if jresp.Error != "" {
		return nil, &Error{hresp.StatusCode, jresp.Error}
	}

	return &jresp, nil
}

func (c *Client) doPoll(ctx context.Context, pollURL string) (string, error) {
	if pollURL == "" {
		return "", errors.New("bad discharge response")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pollURL, nil)
	if err != nil {
		return "", err
	}

	var (
		bo    time.Duration
		jresp jsonResponse
	)

pollLoop:
	for {
		hresp, err := c.http.Do(req)
		if err != nil {
			return "", err
		}

		if hresp.StatusCode == http.StatusAccepted {
			bo = c.nextBO(bo)

			select {
			case <-time.After(bo):
				continue pollLoop
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}

		if err := json.NewDecoder(hresp.Body).Decode(&jresp); err != nil {
			return "", fmt.Errorf("bad response (%d): %w", hresp.StatusCode, err)
		}
		if jresp.Error != "" {
			return "", &Error{hresp.StatusCode, jresp.Error}
		}
		if jresp.Discharge == "" {
			return "", fmt.Errorf("bad response (%d): missing discharge", hresp.StatusCode)
		}

		return jresp.Discharge, nil
	}
}

func (c *Client) doUserInteractive(ctx context.Context, ui *jsonUserInteractive) (string, error) {
	if ui.PollURL == "" || ui.UserURL == "" {
		return "", errors.New("bad discharge response")
	}
	if c.UserURLCallback == nil {
		return "", errors.New("missing user-url callback")
	}

	if err := c.openUserInteractiveURL(ctx, ui.UserURL); err != nil {
		return "", err
	}

	return c.doPoll(ctx, ui.PollURL)
}

func (c *Client) nextBO(lastBO time.Duration) time.Duration {
	if c.PollBackoffNext != nil {
		return c.PollBackoffNext(lastBO)
	}
	if lastBO == 0 {
		return time.Second
	}
	return 2 * lastBO
}

func (c *Client) openUserInteractiveURL(ctx context.Context, url string) error {
	if c.UserURLCallback != nil {
		return c.UserURLCallback(ctx, url)
	}

	return errors.New("client not configured for opening URLs")
}

func initURL(location string) string {
	if strings.HasSuffix(location, "/") {
		return location + InitPath[1:]
	}
	return location + InitPath
}

type Error struct {
	StatusCode int
	Msg        string
}

func (e Error) Error() string {
	return fmt.Sprintf("tp error (%d): %s", e.StatusCode, e.Msg)
}

type authenticatedHTTP struct {
	t    http.RoundTripper
	auth map[string]string
}

func (a *authenticatedHTTP) RoundTrip(r *http.Request) (*http.Response, error) {
	if cred := a.auth[r.URL.Hostname()]; cred != "" {
		r.Header.Set("Authorization", cred)
	}

	return a.transport().RoundTrip(r)
}

func (a *authenticatedHTTP) transport() http.RoundTripper {
	if a.t == nil {
		return http.DefaultTransport
	}
	return a.t
}

func defaultBackoff(lastBO time.Duration) (nextBO time.Duration) {
	if lastBO == 0 {
		return time.Second
	}
	return 2 * lastBO
}
