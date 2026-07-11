package service

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/smtp"
	"os"
	"regexp"
	"strings"
	"time"
)

// Mailer sends transactional email (verification codes, invites).
type Mailer interface {
	Send(ctx context.Context, to, subject, body string) error
}

// LogMailer logs messages — used in dev when SMTP is not configured.
type LogMailer struct {
	Log *slog.Logger
}

var codeRegexp = regexp.MustCompile(`\b\d{6}\b`)

func (m *LogMailer) Send(_ context.Context, to, subject, body string) error {
	log := m.Log
	if log == nil {
		log = slog.Default()
	}
	if strings.ToLower(os.Getenv("ENVIRONMENT")) == "local" {
		if match := codeRegexp.FindString(body); match != "" {
			log.Info("email (dev mailer)", "to", to, "subject", subject, "code", match)
		} else {
			log.Info("email (dev mailer)", "to", to, "subject", subject, "body", body)
		}
	} else {
		log.Info("email (dev mailer)", "to", to, "subject", subject, "bodyBytes", len(body))
	}
	return nil
}

// SMTPMailer delivers email via SMTP (e.g. SendGrid SMTP relay).
//
// SendGrid: host smtp.sendgrid.net, port 587, username "apikey", password = API key.
type SMTPMailer struct {
	Host     string
	Port     string
	Username string
	Password string
	From     string // envelope + header address
	FromName string
	Log      *slog.Logger
}

func (m *SMTPMailer) Send(ctx context.Context, to, subject, body string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	host := strings.TrimSpace(m.Host)
	port := strings.TrimSpace(m.Port)
	if host == "" {
		host = "smtp.sendgrid.net"
	}
	if port == "" {
		port = "587"
	}
	from := strings.TrimSpace(m.From)
	if from == "" {
		return fmt.Errorf("smtp from address is required")
	}
	to = strings.TrimSpace(to)
	if to == "" {
		return fmt.Errorf("smtp recipient is required")
	}

	msg := buildSMTPMessage(m.FromName, from, to, subject, body)
	addr := net.JoinHostPort(host, port)
	auth := smtp.PlainAuth("", m.Username, m.Password, host)

	// smtp.SendMail does not take a context; bound total wait with a deadline goroutine.
	errCh := make(chan error, 1)
	go func() {
		errCh <- smtp.SendMail(addr, auth, from, []string{to}, msg)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("smtp send: %w", err)
		}
		if m.Log != nil {
			m.Log.Info("email sent", "to", to, "subject", subject, "via", addr)
		}
		return nil
	case <-time.After(30 * time.Second):
		return fmt.Errorf("smtp send timed out after 30s")
	}
}

func buildSMTPMessage(fromName, from, to, subject, body string) []byte {
	fromHeader := from
	if strings.TrimSpace(fromName) != "" {
		fromHeader = fmt.Sprintf("%s <%s>", sanitizeHeader(fromName), from)
	}
	contentType := "text/plain; charset=UTF-8"
	if looksLikeHTML(body) {
		contentType = "text/html; charset=UTF-8"
	}
	// CRLF line endings required by SMTP.
	var b strings.Builder
	b.WriteString("From: " + fromHeader + "\r\n")
	b.WriteString("To: " + to + "\r\n")
	b.WriteString("Subject: " + sanitizeHeader(subject) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: " + contentType + "\r\n")
	b.WriteString("\r\n")
	b.WriteString(normalizeBodyNewlines(body))
	return []byte(b.String())
}

func looksLikeHTML(body string) bool {
	lower := strings.ToLower(body)
	return strings.Contains(lower, "<html") || strings.Contains(lower, "<!doctype html")
}

func sanitizeHeader(s string) string {
	// Prevent header injection.
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", "")
	return s
}

func normalizeBodyNewlines(body string) string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\r", "\n")
	return strings.ReplaceAll(body, "\n", "\r\n")
}
