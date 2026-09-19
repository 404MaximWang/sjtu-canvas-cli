package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/mdp/qrterminal/v3"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/404MaximWang/sjtu-canvas-cli/internal/canvas"
	"github.com/404MaximWang/sjtu-canvas-cli/internal/config"
	"github.com/404MaximWang/sjtu-canvas-cli/internal/cred"
	"github.com/404MaximWang/sjtu-canvas-cli/internal/jaccount"
	"github.com/404MaximWang/sjtu-canvas-cli/internal/session"
)

// qrLoginTimeout bounds the whole scan-and-confirm flow; it is human-paced,
// not network-paced.
const qrLoginTimeout = 3 * time.Minute

// newAuthCmd builds `sjtu auth` and its six leaves.
func newAuthCmd(stderr io.Writer) *cobra.Command {
	auth := &cobra.Command{
		Use:   "auth",
		Short: "Manage Canvas and jAccount credentials",
	}
	auth.AddCommand(newAuthCanvasCmd())
	auth.AddCommand(newAuthJAccountCmd(stderr))
	auth.AddCommand(newAuthStatusCmd())
	auth.AddCommand(&cobra.Command{
		Use:   "logout",
		Short: "Remove both the Canvas token and the jAccount session",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return logoutAll(cmd.Context())
		},
	})
	return auth
}

// newAuthCanvasCmd builds `sjtu auth canvas login|logout`.
func newAuthCanvasCmd() *cobra.Command {
	canvasCmd := &cobra.Command{
		Use:   "canvas",
		Short: "Canvas API token (oc.sjtu.edu.cn)",
	}
	var tokenFlag string
	login := &cobra.Command{
		Use:   "login",
		Short: "Store a Canvas API token after verifying it",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return canvasLogin(cmd.Context(), tokenFlag)
		},
	}
	login.Flags().StringVar(&tokenFlag, "token", "", "Canvas API token (omit to paste interactively)")
	canvasCmd.AddCommand(login)
	canvasCmd.AddCommand(&cobra.Command{
		Use:   "logout",
		Short: "Remove the stored Canvas API token",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return logout(cmd.Context(), cred.KeyCanvas, "Canvas token")
		},
	})
	return canvasCmd
}

// newAuthJAccountCmd builds `sjtu auth jaccount login|logout`.
func newAuthJAccountCmd(stderr io.Writer) *cobra.Command {
	jaccountCmd := &cobra.Command{
		Use:   "jaccount",
		Short: "jAccount SSO session (QR-code login)",
	}
	jaccountCmd.AddCommand(&cobra.Command{
		Use:   "login",
		Short: "Log in via jAccount QR code",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return jaccountLogin(cmd.Context(), ensureStderr(stderr))
		},
	})
	jaccountCmd.AddCommand(&cobra.Command{
		Use:   "logout",
		Short: "Remove the stored jAccount session cookie",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return logout(cmd.Context(), cred.KeyJAccount, "jAccount session")
		},
	})
	return jaccountCmd
}

// newAuthStatusCmd builds `sjtu auth status`.
func newAuthStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show which credentials are stored and whether they still work",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return authStatus(cmd.Context())
		},
	}
}

// openStore opens the credential store with a uniform error message.
func openStore() (cred.Store, error) {
	store, err := cred.Open()
	if err != nil {
		return nil, fmt.Errorf("open credential store: %w", err)
	}
	return store, nil
}

// canvasLogin implements `sjtu auth canvas login`: obtain a token (flag or
// hidden prompt), verify it against Canvas, then store it.
func canvasLogin(ctx context.Context, tokenFlag string) error {
	token := strings.TrimSpace(tokenFlag)
	if token == "" {
		var err error
		token, err = promptHidden("Paste Canvas API token: ")
		if err != nil {
			return err
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	me, err := canvas.VerifyToken(ctx, cfg.CanvasBaseURL, token)
	if err != nil {
		return fmt.Errorf("token rejected by Canvas: %w", err)
	}
	store, err := openStore()
	if err != nil {
		return err
	}
	if err := store.Set(cred.KeyCanvas, token); err != nil {
		return fmt.Errorf("store token: %w", err)
	}
	session.RegisterSecret(token)
	fmt.Printf("Canvas login OK: %s (id %d), stored via %s\n", me.Name, me.ID, store.Backend())
	return nil
}

// promptHidden reads one line without terminal echo, for interactive token
// entry. Non-terminal stdin (scripts, pipes) must use --token instead.
func promptHidden(prompt string) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", errors.New("stdin is not a terminal; pass --token instead")
	}
	fmt.Fprint(os.Stderr, prompt)
	raw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read token: %w", err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", errors.New("empty token")
	}
	return token, nil
}

// jaccountLogin implements `sjtu auth jaccount login`: render the QR code,
// wait for the scan, verify the issued cookie, then store it.
func jaccountLogin(ctx context.Context, stderr io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, qrLoginTimeout)
	defer cancel()
	first := true
	cookie, err := jaccount.Login(ctx, func(url string) {
		if !first {
			fmt.Fprintln(stderr, "QR code refreshed; the previous one expired.")
		}
		first = false
		fmt.Fprintln(stderr, "Scan with the SJTU app (交我办):")
		qrterminal.GenerateHalfBlock(url, qrterminal.L, os.Stderr)
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return failf("login canceled")
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return failf("login timed out; run `sjtu auth jaccount login` to retry")
		}
		return fmt.Errorf("jaccount login: %w", err)
	}
	if err := jaccount.Verify(ctx, cookie); err != nil {
		return fmt.Errorf("session cookie failed verification: %w", err)
	}
	store, err := openStore()
	if err != nil {
		return err
	}
	if err := store.Set(cred.KeyJAccount, cookie); err != nil {
		return fmt.Errorf("store cookie: %w", err)
	}
	session.RegisterSecret(cookie)
	fmt.Printf("jAccount login OK, session stored via %s\n", store.Backend())
	return nil
}

// getCredential reads a credential and arms the log redactor with it. Every
// credential entering process memory must be registered immediately: the
// redactor is per-process, so values read from the store in commands other
// than login would otherwise be unprotected if anything ever logged them.
func getCredential(store cred.Store, key string) (string, error) {
	value, err := store.Get(key)
	if err != nil {
		return "", err
	}
	session.RegisterSecret(value)
	return value, nil
}

// authStatus implements `sjtu auth status`: list each credential, where it
// lives, and whether the upstream service still accepts it.
func authStatus(ctx context.Context) error {
	store, err := openStore()
	if err != nil {
		return err
	}
	fmt.Printf("credential backend: %s\n", store.Backend())
	reportCanvas(ctx, store)
	reportJAccount(ctx, store)
	return nil
}

// reportCanvas prints the Canvas token's presence and live validity.
func reportCanvas(ctx context.Context, store cred.Store) {
	token, err := getCredential(store, cred.KeyCanvas)
	if errors.Is(err, cred.ErrNotFound) {
		fmt.Println("canvas:   not configured")
		return
	}
	if err != nil {
		fmt.Printf("canvas:   read error: %v\n", err)
		return
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Printf("canvas:   config error: %v\n", err)
		return
	}
	me, err := canvas.VerifyToken(ctx, cfg.CanvasBaseURL, token)
	if err != nil {
		fmt.Println("canvas:   stored but rejected (expired or revoked)")
		return
	}
	fmt.Printf("canvas:   valid, %s (id %d)\n", me.Name, me.ID)
}

// reportJAccount prints the jAccount cookie's presence and live validity.
func reportJAccount(ctx context.Context, store cred.Store) {
	cookie, err := getCredential(store, cred.KeyJAccount)
	if errors.Is(err, cred.ErrNotFound) {
		fmt.Println("jaccount: not configured")
		return
	}
	if err != nil {
		fmt.Printf("jaccount: read error: %v\n", err)
		return
	}
	if err := jaccount.Verify(ctx, cookie); err != nil {
		fmt.Println("jaccount: stored but rejected (session expired)")
		return
	}
	fmt.Println("jaccount: valid")
}

// logout removes one credential and reports what happened.
func logout(_ context.Context, key, label string) error {
	store, err := openStore()
	if err != nil {
		return err
	}
	if err := store.Delete(key); err != nil {
		return fmt.Errorf("remove %s: %w", label, err)
	}
	fmt.Printf("%s removed\n", label)
	return nil
}

// logoutAll implements `sjtu auth logout` without a subcommand: drop both
// credentials.
func logoutAll(ctx context.Context) error {
	if err := logout(ctx, cred.KeyCanvas, "Canvas token"); err != nil {
		return err
	}
	return logout(ctx, cred.KeyJAccount, "jAccount session")
}
