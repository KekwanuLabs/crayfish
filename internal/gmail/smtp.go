package gmail

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/smtp"
	"strings"
	"time"
)

const (
	smtpAddr    = "smtp.gmail.com:587"
	smtpHost    = "smtp.gmail.com"
	smtpTimeout = 15 * time.Second
)

// SMTPClient sends emails via Gmail SMTP with App Password auth.
type SMTPClient struct {
	email       string
	appPassword string
	logger      *slog.Logger
}

// NewSMTPClient creates a new Gmail SMTP sender.
func NewSMTPClient(email, appPassword string, logger *slog.Logger) *SMTPClient {
	return &SMTPClient{
		email:       email,
		appPassword: appPassword,
		logger:      logger,
	}
}

// SendReply sends a reply to an email. Sets In-Reply-To and References headers
// for proper threading in Gmail.
func (s *SMTPClient) SendReply(to, subject, body, inReplyTo string) error {
	// Build RFC 5322 message.
	var msg strings.Builder
	msg.WriteString(fmt.Sprintf("From: %s\r\n", s.email))
	msg.WriteString(fmt.Sprintf("To: %s\r\n", to))
	msg.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))
	if inReplyTo != "" {
		msg.WriteString(fmt.Sprintf("In-Reply-To: %s\r\n", inReplyTo))
		msg.WriteString(fmt.Sprintf("References: %s\r\n", inReplyTo))
	}
	msg.WriteString("MIME-Version: 1.0\r\n")
	msg.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	msg.WriteString("\r\n")
	msg.WriteString(body)

	// Connect with timeout.
	conn, err := net.DialTimeout("tcp", smtpAddr, smtpTimeout)
	if err != nil {
		return fmt.Errorf("gmail.SMTP dial: %w", err)
	}

	c, err := smtp.NewClient(conn, smtpHost)
	if err != nil {
		conn.Close()
		return fmt.Errorf("gmail.SMTP client: %w", err)
	}
	defer c.Close()

	// STARTTLS.
	if err := c.StartTLS(&tls.Config{ServerName: smtpHost}); err != nil {
		return fmt.Errorf("gmail.SMTP starttls: %w", err)
	}

	// Auth with App Password.
	auth := smtp.PlainAuth("", s.email, s.appPassword, smtpHost)
	if err := c.Auth(auth); err != nil {
		return fmt.Errorf("gmail.SMTP auth: %w", err)
	}

	// Set sender and recipient.
	if err := c.Mail(s.email); err != nil {
		return fmt.Errorf("gmail.SMTP mail from: %w", err)
	}

	// Parse multiple recipients.
	recipients := strings.Split(to, ",")
	for _, rcpt := range recipients {
		rcpt = strings.TrimSpace(rcpt)
		// Extract bare email if in "Name <email>" format.
		if idx := strings.Index(rcpt, "<"); idx >= 0 {
			end := strings.Index(rcpt, ">")
			if end > idx {
				rcpt = rcpt[idx+1 : end]
			}
		}
		if err := c.Rcpt(rcpt); err != nil {
			return fmt.Errorf("gmail.SMTP rcpt %s: %w", rcpt, err)
		}
	}

	// Write message body.
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("gmail.SMTP data: %w", err)
	}
	if _, err := w.Write([]byte(msg.String())); err != nil {
		return fmt.Errorf("gmail.SMTP write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("gmail.SMTP close data: %w", err)
	}

	s.logger.Info("email sent", "to", maskEmail(to), "subject", subject)
	return c.Quit()
}

// Send sends a new email (not a reply).
func (s *SMTPClient) Send(to, subject, body string) error {
	return s.SendReply(to, subject, body, "")
}
