package source

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
)

// historyLimit mirrors the "up to ~20" newest posts the web source returns.
const historyLimit = 20

// MTProtoSource reads public channels by logging in as a real Telegram
// user via MTProto. Bots cannot read channels they don't administer, so
// this authenticates as a user (telegram/auth.Flow) and never joins the
// channel — contacts.resolveUsername + messages.getHistory work on any
// public channel without joining.
//
// It reconnects for every Fetch call, reusing the same on-disk session
// (no re-login). That is simpler than holding one long-lived connection
// across every channel in a run, at the cost of one extra handshake per
// channel — acceptable for a job that runs every 30 minutes.
type MTProtoSource struct {
	AppID       int
	AppHash     string
	SessionFile string
}

// NewMTProtoSource validates the numeric api_id and builds a source.
func NewMTProtoSource(apiID, apiHash, sessionFile string) (*MTProtoSource, error) {
	id, err := strconv.Atoi(strings.TrimSpace(apiID))
	if err != nil {
		return nil, fmt.Errorf("source.telegram.api_id must be numeric, got %q: %w", apiID, err)
	}
	if apiHash == "" {
		return nil, fmt.Errorf("source.telegram.api_hash is empty")
	}
	if sessionFile == "" {
		return nil, fmt.Errorf("source.telegram.session_file is empty")
	}
	return &MTProtoSource{AppID: id, AppHash: apiHash, SessionFile: sessionFile}, nil
}

func (s *MTProtoSource) Fetch(ctx context.Context, channel string) ([]Post, error) {
	if _, err := os.Stat(s.SessionFile); err != nil && !isInteractiveTerminal() {
		return nil, fmt.Errorf("mtproto: session file %s is missing and this is a non-interactive environment (e.g. CI) — "+
			"run the login once locally with `mode: mtproto` on an interactive terminal, then copy %s here", s.SessionFile, s.SessionFile)
	}

	client := telegram.NewClient(s.AppID, s.AppHash, telegram.Options{
		SessionStorage: &session.FileStorage{Path: s.SessionFile},
	})

	var posts []Post
	err := client.Run(ctx, func(ctx context.Context) error {
		status, err := client.Auth().Status(ctx)
		if err != nil {
			return fmt.Errorf("checking auth status: %w", err)
		}
		if !status.Authorized {
			if !isInteractiveTerminal() {
				return fmt.Errorf("mtproto: not authorized and no terminal available to log in — " +
					"run this once on an interactive machine to create the session file, then reuse it here")
			}
			flow := auth.NewFlow(&stdinAuthenticator{}, auth.SendCodeOptions{})
			if err := client.Auth().IfNecessary(ctx, flow); err != nil {
				return fmt.Errorf("login: %w", err)
			}
		}

		api := client.API()
		resolved, err := api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: channel})
		if err != nil {
			return fmt.Errorf("resolving @%s: %w", channel, err)
		}

		peer, err := inputPeerFor(resolved)
		if err != nil {
			return fmt.Errorf("resolving @%s: %w", channel, err)
		}

		history, err := api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
			Peer:  peer,
			Limit: historyLimit,
		})
		if err != nil {
			return fmt.Errorf("fetching history for %s: %w", channel, err)
		}

		msgs, err := messagesOf(history)
		if err != nil {
			return fmt.Errorf("fetching history for %s: %w", channel, err)
		}

		for _, m := range msgs {
			msg, ok := m.(*tg.Message)
			if !ok {
				continue // service message, not a post
			}
			text := strings.TrimSpace(msg.GetMessage())
			if text == "" {
				continue // pure media post with no caption
			}
			id := msg.GetID()
			posts = append(posts, Post{
				Channel:   channel,
				MessageID: int64(id),
				Text:      text,
				URL:       fmt.Sprintf("https://t.me/%s/%d", channel, id),
				PostedAt:  time.Unix(int64(msg.GetDate()), 0).UTC(),
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return posts, nil
}

// inputPeerFor turns a resolved username into the InputPeer needed by
// messages.getHistory, using the access hash from the accompanying
// chats/users list.
func inputPeerFor(resolved *tg.ContactsResolvedPeer) (tg.InputPeerClass, error) {
	switch p := resolved.Peer.(type) {
	case *tg.PeerChannel:
		for _, c := range resolved.Chats {
			if ch, ok := c.(*tg.Channel); ok && ch.GetID() == p.ChannelID {
				hash, _ := ch.GetAccessHash()
				return &tg.InputPeerChannel{ChannelID: ch.GetID(), AccessHash: hash}, nil
			}
		}
		return nil, fmt.Errorf("resolved channel id %d not found in response", p.ChannelID)
	case *tg.PeerUser:
		for _, u := range resolved.Users {
			if user, ok := u.(*tg.User); ok && user.GetID() == p.UserID {
				hash, _ := user.GetAccessHash()
				return &tg.InputPeerUser{UserID: user.GetID(), AccessHash: hash}, nil
			}
		}
		return nil, fmt.Errorf("resolved user id %d not found in response", p.UserID)
	default:
		return nil, fmt.Errorf("unsupported peer type %T (expected a channel)", p)
	}
}

// messagesOf extracts the []MessageClass slice regardless of which of the
// three non-empty messages.Messages variants the server chose to return.
func messagesOf(m tg.MessagesMessagesClass) ([]tg.MessageClass, error) {
	switch v := m.(type) {
	case *tg.MessagesMessages:
		return v.Messages, nil
	case *tg.MessagesMessagesSlice:
		return v.Messages, nil
	case *tg.MessagesChannelMessages:
		return v.Messages, nil
	case *tg.MessagesMessagesNotModified:
		return nil, nil
	default:
		return nil, fmt.Errorf("unexpected messages.Messages variant %T", m)
	}
}

func isInteractiveTerminal() bool {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (stat.Mode() & os.ModeCharDevice) != 0
}

// stdinAuthenticator implements auth.UserAuthenticator by prompting on
// stdin/stdout. Used only for the one-time interactive login; every
// later run loads the persisted session file instead.
type stdinAuthenticator struct{}

func (stdinAuthenticator) prompt(label string) (string, error) {
	fmt.Fprint(os.Stderr, label)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func (a *stdinAuthenticator) Phone(_ context.Context) (string, error) {
	return a.prompt("Telegram phone number (with country code, e.g. +15551234567): ")
}

func (a *stdinAuthenticator) Password(_ context.Context) (string, error) {
	return a.prompt("Telegram 2FA password: ")
}

func (a *stdinAuthenticator) AcceptTermsOfService(_ context.Context, tos tg.HelpTermsOfService) error {
	fmt.Fprintln(os.Stderr, "Telegram Terms of Service:", tos.Text)
	return nil
}

func (a *stdinAuthenticator) SignUp(_ context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, fmt.Errorf("this phone number has no Telegram account — sign-up is not supported, log in with an existing account")
}

func (a *stdinAuthenticator) Code(_ context.Context, _ *tg.AuthSentCode) (string, error) {
	return a.prompt("Login code sent by Telegram: ")
}
