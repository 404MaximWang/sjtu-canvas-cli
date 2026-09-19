// Package jaccount implements QR-code login against SJTU's jAccount SSO and
// verification of the resulting session cookie.
//
// Protocol, reverse-engineered from the SJTU Canvas Helper project:
//
//  1. GET my.sjtu.edu.cn/ui/appmyinfo anonymously; the login page HTML
//     embeds a UUID identifying this login attempt.
//  2. Open wss://jaccount.sjtu.edu.cn/jaccount/sub/{uuid} and ask for
//     UPDATE_QR_CODE; the server answers with a (ts, sig) pair that turns
//     the UUID into a scannable URL.
//  3. The user scans that URL with the SJTU app; the server pushes LOGIN.
//  4. GET jaccount.sjtu.edu.cn/jaccount/expresslogin?uuid=… completes the
//     SSO exchange and plants JAAuthCookie in the session jar.
package jaccount

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"

	"github.com/gorilla/websocket"

	"github.com/404MaximWang/sjtu-canvas-cli/internal/session"
)

// SSO endpoints. These are stable jAccount infrastructure URLs, not Canvas.
const (
	portalURL       = "https://my.sjtu.edu.cn/ui/appmyinfo"
	accountAPIURL   = "https://my.sjtu.edu.cn/api/account"
	expressLoginURL = "https://jaccount.sjtu.edu.cn/jaccount/expresslogin"
	qrContentURL    = "https://jaccount.sjtu.edu.cn/jaccount/confirmscancode"
	websocketBase   = "wss://jaccount.sjtu.edu.cn/jaccount/sub"
)

// cookieName is the SSO session cookie issued after a successful scan.
const cookieName = "JAAuthCookie"

// cookieDomains are the hosts the JAAuthCookie must be seeded for when a
// stored credential is restored; both participate in the SSO chain.
var cookieDomains = []string{"jaccount.sjtu.edu.cn", "my.sjtu.edu.cn"}

// uuidPattern extracts the login UUID from the portal's login page. The UUID
// sits inside an inline <script> rather than a DOM attribute, so an HTML
// parser cannot reach it; a regexp over the raw page is the correct tool.
var uuidPattern = regexp.MustCompile(
	`uuid\s*[:=]\s*["']?([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})["']?`)

// qrRefreshInterval matches the server-side QR signature rotation cadence.
const qrRefreshInterval = 25 * time.Second

// updateQRCodeMessage asks the server for a fresh QR signature.
const updateQRCodeMessage = `{ "type": "UPDATE_QR_CODE" }`

// loginMessage mirrors the server's WebSocket frame shape; fields not needed
// by the flow are left unmapped on purpose.
type loginMessage struct {
	Type    string `json:"type"`
	Payload struct {
		TS  int64  `json:"ts"`
		Sig string `json:"sig"`
	} `json:"payload"`
}


// Login runs the QR-code login flow and returns the issued JAAuthCookie.
// onQR is invoked with the scannable URL each time the server rotates the QR
// signature; the caller decides how to render it. ctx bounds the whole flow
// (scanning takes a human-scale amount of time; pick timeouts accordingly).
func Login(ctx context.Context, onQR func(url string)) (string, error) {
	s, err := session.NewCookies()
	if err != nil {
		return "", err
	}
	uuid, err := fetchUUID(ctx, s)
	if err != nil {
		return "", err
	}
	cookie, err := awaitScan(ctx, s, uuid, onQR)
	if err != nil {
		return "", err
	}
	return cookie, nil
}

// fetchUUID loads the anonymous portal login page and extracts the login
// attempt UUID.
func fetchUUID(ctx context.Context, s *session.Session) (string, error) {
	resp, err := s.DoStream(ctx, "GET", portalURL)
	if err != nil {
		return "", fmt.Errorf("fetch login page: %w", err)
	}
	defer resp.Body.Close()
	// The page is a small HTML document; cap reads defensively anyway.
	page, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", fmt.Errorf("read login page: %w", err)
	}
	match := uuidPattern.FindSubmatch(page)
	if match == nil {
		return "", errors.New("no login UUID in portal page (already signed in upstream?)")
	}
	return string(match[1]), nil
}

// awaitScan drives the WebSocket half of the login flow until the server
// confirms the scan, then completes the express-login exchange.
func awaitScan(ctx context.Context, s *session.Session, uuid string, onQR func(string)) (string, error) {
	dialer := websocket.Dialer{Jar: s.Jar()}
	conn, _, err := dialer.DialContext(ctx, websocketBase+"/"+uuid, nil)
	if err != nil {
		return "", fmt.Errorf("connect login websocket: %w", err)
	}
	defer conn.Close()

	if err := conn.WriteMessage(websocket.TextMessage, []byte(updateQRCodeMessage)); err != nil {
		return "", fmt.Errorf("request QR code: %w", err)
	}

	refresh := time.NewTicker(qrRefreshInterval)
	defer refresh.Stop()

	// The server pushes frames at its own pace; reads therefore run on a
	// helper goroutine while the main goroutine owns the refresh ticker.
	messages := make(chan loginMessage, 4)
	readErr := make(chan error, 1)
	go func() {
		for {
			var msg loginMessage
			if err := conn.ReadJSON(&msg); err != nil {
				readErr <- err
				return
			}
			messages <- msg
		}
	}()

	for {
		select {
		case <-ctx.Done():
			// Propagate the raw cause so callers can distinguish Ctrl-C
			// (context.Canceled) from the login deadline (DeadlineExceeded).
			return "", ctx.Err()
		case err := <-readErr:
			return "", fmt.Errorf("login websocket: %w", err)
		case <-refresh.C:
			if err := conn.WriteMessage(websocket.TextMessage, []byte(updateQRCodeMessage)); err != nil {
				return "", fmt.Errorf("refresh QR code: %w", err)
			}
		case msg := <-messages:
			switch msg.Type {
			case "UPDATE_QR_CODE":
				onQR(fmt.Sprintf("%s?uuid=%s&ts=%d&sig=%s",
					qrContentURL, uuid, msg.Payload.TS, msg.Payload.Sig))
			case "LOGIN":
				return completeExpressLogin(ctx, s, uuid)
			}
		}
	}
}

// completeExpressLogin performs the final GET that plants JAAuthCookie into
// the session jar after a confirmed scan.
func completeExpressLogin(ctx context.Context, s *session.Session, uuid string) (string, error) {
	if err := s.DoJSON(ctx, "GET", expressLoginURL+"?uuid="+uuid, nil); err != nil {
		return "", fmt.Errorf("express login: %w", err)
	}
	cookie, ok := s.Cookie("https://jaccount.sjtu.edu.cn", cookieName)
	if !ok {
		return "", errors.New("express login completed but no JAAuthCookie was issued")
	}
	return cookie, nil
}

// Verify checks that a stored JAAuthCookie still authenticates against the
// portal. The portal's app page must be requested first: the SSO chain only
// establishes the my.sjtu.edu.cn session on that warmup request, and probing
// the API directly can report unauthenticated even for a valid cookie.
func Verify(ctx context.Context, cookie string) error {
	s, err := session.NewCookies()
	if err != nil {
		return err
	}
	s.SeedCookie(cookieName, cookie, cookieDomains...)
	if err := s.DoJSON(ctx, "GET", portalURL, nil); err != nil {
		return fmt.Errorf("warm up SSO chain: %w", err)
	}
	return s.DoJSON(ctx, "GET", accountAPIURL, nil)
}
