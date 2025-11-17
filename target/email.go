package target

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// connects via SMTPS -> builds RFC‑822 email with HTML body -> sends as one message
func SendToEmail(
	ctx context.Context,
	email,
	subject,
	smtpHost string,
	smtpPort int, password,
	bodyHTML string,
) error {
	headers := map[string]string{
		"From":         email,
		"To":           email,
		"Subject":      subject,
		"MIME-Version": "1.0",
		"Content-Type": "text/html; charset=\"utf-8\"",
	}

	// join headers into the message
	var msgBuilder strings.Builder
	for k, v := range headers {
		msgBuilder.WriteString(fmt.Sprintf("%s: %s\r\n", k, v))
	}
	msgBuilder.WriteString("\r\n") // header/body separator
	msgBuilder.WriteString(bodyHTML)

	rawMsg := []byte(msgBuilder.String())

	// tls
	addr := fmt.Sprintf("%s:%d", smtpHost, smtpPort)
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
		ServerName: smtpHost,
	})
	if err != nil {
		return fmt.Errorf("smtp dial error: %w", err)
	}
	defer conn.Close()

	// smtp
	client, err := smtp.NewClient(conn, smtpHost)
	if err != nil {
		return fmt.Errorf("creating smtp client: %w", err)
	}
	defer client.Quit()

	// authenticate
	auth := smtp.PlainAuth("", email, password, smtpHost)
	if err := client.Auth(auth); err != nil {
		return fmt.Errorf("smtp auth error: %w", err)
	}

	// mail from
	if err := client.Mail(email); err != nil {
		return fmt.Errorf("smtp MAIL FROM error: %w", err)
	}
	// rcpt
	if err := client.Rcpt(email); err != nil {
		return fmt.Errorf("smtp RCPT TO error: %w", err)
	}

	// data
	wc, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA error: %w", err)
	}
	if _, err := wc.Write(rawMsg); err != nil {
		return fmt.Errorf("writing message data: %w", err)
	}
	if err := wc.Close(); err != nil {
		return fmt.Errorf("closing data writer: %w", err)
	}

	return nil
}
