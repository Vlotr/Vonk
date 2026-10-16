package auth

import (
	"context"
	"fmt"
	netmail "net/mail"
	"strings"
	"time"

	"monitor/internal/mail"
	"monitor/internal/store"
)

// InputError is a problem with what someone typed; its text is shown to them.
type InputError string

func (e InputError) Error() string { return string(e) }

// CheckEmail returns an InputError unless email is one plain address such
// as jan@example.com (no name, no angle brackets). Pass it normalized
// (store.NormalizeEmail).
func CheckEmail(email string) error {
	addr, err := netmail.ParseAddress(email)
	if err != nil || addr.Name != "" || addr.Address != email || len(email) > 254 {
		return InputError("Enter one email address, such as jan@example.com.")
	}
	return nil
}

// mailTimeout limits sending one invite or reset mail.
const mailTimeout = 20 * time.Second

// InviteLink is a new invite and the link that accepts it. The link holds
// the only copy of the token, so it can be shown once only. The token
// follows "#token=" in the link, the part browsers never send to a server.
type InviteLink struct {
	Invite store.Invite
	URL    string
	// Mailed reports whether the link was also mailed to the invited address.
	Mailed bool
}

// Invite makes a link with which email can create an account with role (and,
// for a member, access to projectIDs). It works once, for 7 days. When mail
// is on it is also mailed. Earlier invites for the address stop working.
// Errors: an InputError, store.ErrEmailTaken, store.ErrUnknownProject or
// store.ErrNotAdmin (byUserID is no longer an admin).
func (s *Service) Invite(ctx context.Context, byUserID int64, email, role string, projectIDs []int64) (InviteLink, error) {
	email = store.NormalizeEmail(email)
	if err := CheckEmail(email); err != nil {
		return InviteLink{}, err
	}
	if !store.ValidRole(role) {
		return InviteLink{}, InputError(`The role must be "admin" or "member".`)
	}
	token, err := newToken()
	if err != nil {
		return InviteLink{}, err
	}
	now := s.now()
	inv, err := s.db.CreateInvite(ctx, store.NewInvite{
		Email: email, Role: role, ProjectIDs: projectIDs, TokenHash: s.hash(token),
		CreatedBy: byUserID, Now: now, ExpiresAt: now.Add(inviteTTL),
	})
	if err != nil {
		return InviteLink{}, err
	}
	link := InviteLink{Invite: inv, URL: s.baseURL + "/invite#token=" + token}
	if s.mailer != nil {
		inviter := "An admin"
		if by, err := s.db.UserByID(ctx, byUserID); err == nil {
			inviter = by.Email
		}
		link.Mailed = s.send(ctx, s.inviteMail(inv, inviter, link.URL))
	}
	return link, nil
}

// ResetLink is a link that lets a user choose a new password.
type ResetLink struct {
	URL       string
	ExpiresAt time.Time
	// Mailed reports whether the link was also mailed to the user.
	Mailed bool
}

// ResetLink makes a link that lets the user choose a new password, once,
// within an hour; using it signs the user out everywhere. Earlier links stop
// working. With mailIt (and mail on) it is also mailed to them; without,
// the caller hands it over: an admin in Settings, or the CLI.
func (s *Service) ResetLink(ctx context.Context, userID int64, mailIt bool) (ResetLink, error) {
	u, err := s.db.UserByID(ctx, userID)
	if err != nil {
		return ResetLink{}, err
	}
	link, err := s.newResetLink(ctx, u)
	if err != nil {
		return ResetLink{}, err
	}
	if mailIt && s.mailer != nil {
		link.Mailed = s.send(ctx, s.resetMail(u, link, "An admin made a link to reset the password of"))
	}
	return link, nil
}

func (s *Service) newResetLink(ctx context.Context, u store.User) (ResetLink, error) {
	token, err := newToken()
	if err != nil {
		return ResetLink{}, err
	}
	now := s.now()
	link := ResetLink{URL: s.baseURL + "/reset-password#token=" + token, ExpiresAt: now.Add(resetTTL)}
	if err := s.db.CreatePasswordReset(ctx, u.ID, s.hash(token), now, link.ExpiresAt); err != nil {
		return ResetLink{}, err
	}
	return link, nil
}

// mailForgotten mails a reset link to the account with this address, if
// there is one. It runs in the background, so the answer to "forgot
// password" takes as long whether or not the address has an account.
func (s *Service) mailForgotten(email string) {
	s.mailWG.Add(1)
	go func() {
		defer s.mailWG.Done()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		u, err := s.db.UserByEmail(ctx, email)
		if err != nil {
			return // no account (or the database is down): nothing to send
		}
		link, err := s.newResetLink(ctx, u)
		if err != nil {
			s.log.Error("auth: make reset link", "err", err)
			return
		}
		s.send(ctx, s.resetMail(u, link, "Someone asked to reset the password of"))
	}()
}

// waitForMail waits until mail sent in the background is done (for tests).
func (s *Service) waitForMail() {
	s.mailWG.Wait()
}

// send mails msg and reports whether that worked; a failure is logged.
func (s *Service) send(ctx context.Context, msg mail.Message) bool {
	ctx, cancel := context.WithTimeout(ctx, mailTimeout)
	defer cancel()
	if err := s.mailer.Send(ctx, msg); err != nil {
		s.log.Warn("auth: send mail", "subject", msg.Subject, "err", err)
		return false
	}
	return true
}

func (s *Service) inviteMail(inv store.Invite, inviter, link string) mail.Message {
	role := "a member"
	if inv.Role == store.RoleAdmin {
		role = "an admin"
	}
	text := strings.Join([]string{
		"Hello,",
		"",
		fmt.Sprintf("%s invited you to the monitoring dashboard at %s, as %s.", inviter, s.baseURL, role),
		"Choose a password to create your account:",
		"",
		link,
		"",
		fmt.Sprintf("The link works once, until %s.", inv.ExpiresAt.UTC().Format("Jan 2, 2006 15:04 UTC")),
	}, "\n")
	return mail.Message{To: inv.Email, Subject: "Your invite to " + s.host, Text: text}
}

func (s *Service) resetMail(u store.User, link ResetLink, why string) mail.Message {
	text := strings.Join([]string{
		"Hello,",
		"",
		fmt.Sprintf("%s %s on %s. Choose a new password here:", why, u.Email, s.baseURL),
		"",
		link.URL,
		"",
		"The link works once, for one hour, and signs you out everywhere else.",
		"If you did not ask for this, ignore this mail: your password stays as it is.",
	}, "\n")
	return mail.Message{To: u.Email, Subject: "Reset your password for " + s.host, Text: text}
}
