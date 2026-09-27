package auth

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type Mailer interface {
	Send(ctx context.Context, to, subject, body string) error
}
type SMTPConfig struct {
	Address, Username, Password, From string
	StartTLS                          bool
}
type SMTPMailer struct{ Config SMTPConfig }

func (m *SMTPMailer) Send(ctx context.Context, to, subject, body string) error {
	c := m.Config
	sender, err := mail.ParseAddress(c.From)
	if err != nil {
		return errors.New("invalid SMTP sender")
	}
	recipient, err := mail.ParseAddress(to)
	if err != nil {
		return errors.New("invalid recipient")
	}
	if strings.ContainsAny(subject, "\r\n") {
		return errors.New("invalid subject")
	}
	host, _, err := net.SplitHostPort(c.Address)
	if err != nil {
		return errors.New("invalid SMTP address")
	}
	conn, err := (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, "tcp", c.Address)
	if err != nil {
		return err
	}
	defer conn.Close()
	deadline := time.Now().Add(30 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer client.Close()
	if c.StartTLS {
		if err = client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			return err
		}
	}
	if c.Username != "" {
		if !c.StartTLS {
			return errors.New("SMTP credentials require STARTTLS")
		}
		if err = client.Auth(smtp.PlainAuth("", c.Username, c.Password, host)); err != nil {
			return err
		}
	}
	if err = client.Mail(sender.Address); err != nil {
		return err
	}
	if err = client.Rcpt(recipient.Address); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s\r\n", sender.String(), recipient.String(), subject, strings.ReplaceAll(body, "\n", "\r\n"))
	if _, err = writer.Write([]byte(msg)); err != nil {
		return err
	}
	if err = writer.Close(); err != nil {
		return err
	}
	return client.Quit()
}

func (s *Service) encryptBody(body string) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return s.aead.Seal(nonce, nonce, []byte(body), nil), nil
}
func (s *Service) decryptBody(v []byte) (string, error) {
	n := s.aead.NonceSize()
	if len(v) < n {
		return "", errors.New("invalid mail envelope")
	}
	plain, err := s.aead.Open(nil, v[:n], v[n:], nil)
	return string(plain), err
}

func (s *Service) enqueueMail(ctx context.Context, tx pgx.Tx, userID, to, subject, body string) error {
	encrypted, err := s.encryptBody(body)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO auth_mail_outbox(id,user_id,recipient,subject,encrypted_body) VALUES($1,$2,$3,$4,$5)`, newID(), userID, to, subject, encrypted)
	return err
}

func (s *Service) issueToken(ctx context.Context, tx pgx.Tx, userID, to, purpose string, ttl time.Duration) error {
	// The caller holds the user's row lock, serializing resend and token rotation.
	var recent bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM auth_email_tokens WHERE user_id=$1 AND purpose=$2 AND created_at>now()-interval '60 seconds')`, userID, purpose).Scan(&recent); err != nil {
		return err
	}
	if recent {
		return nil
	}
	token, err := randomToken()
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE auth_email_tokens SET consumed_at=now() WHERE user_id=$1 AND purpose=$2 AND consumed_at IS NULL`, userID, purpose); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO auth_email_tokens(id,user_id,purpose,token_hash,target_email,expires_at) VALUES($1,$2,$3,$4,$5,$6)`, newID(), userID, purpose, hashSecret(token), to, time.Now().Add(ttl)); err != nil {
		return err
	}
	route := map[string]string{"verify": "verify-email", "reset": "reset-password", "email_change": "confirm-email"}[purpose]
	// A URL fragment keeps the bearer token out of HTTP access/referrer logs.
	link := strings.TrimRight(s.cfg.BaseURL, "/") + "/" + route + "#token=" + url.QueryEscape(token)
	body := "请打开以下链接并在页面确认操作。仅打开链接不会消费令牌。\n\n" + link + "\n\n如果不是您发起的请求，请忽略本邮件。此链接仅可使用一次。"
	return s.enqueueMail(ctx, tx, userID, to, "OpenMajiang account confirmation", body)
}

// StartMailWorker runs until ctx is cancelled. The transactional outbox uses a
// lease so a crashed sender is retried; delivery is at-least-once and links remain
// single-use. No bearer token, message body, SMTP password or raw SMTP error logs.
func (s *Service) StartMailWorker(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		for i := 0; i < 20; i++ {
			ok := s.deliverOne(ctx)
			if !ok {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) deliverOne(ctx context.Context) bool {
	var id, to, subject string
	var body []byte
	var attempts int
	err := s.pool.QueryRow(ctx, `UPDATE auth_mail_outbox SET status='sending',attempts=attempts+1,available_at=now()+interval '2 minutes' WHERE id=(SELECT id FROM auth_mail_outbox WHERE status IN ('queued','sending') AND available_at<=now() ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING id,recipient,subject,encrypted_body,attempts`).Scan(&id, &to, &subject, &body, &attempts)
	if err != nil {
		return false
	}
	plain, err := s.decryptBody(body)
	if err == nil {
		err = s.cfg.Mailer.Send(ctx, to, subject, plain)
	}
	if err == nil {
		_, _ = s.pool.Exec(ctx, `UPDATE auth_mail_outbox SET status='sent',sent_at=now(),encrypted_body=''::bytea,last_error='' WHERE id=$1 AND status='sending' AND attempts=$2`, id, attempts)
		return true
	}
	status := "queued"
	if attempts >= 10 {
		status = "failed"
	}
	delay := time.Duration(1<<min(attempts, 10)) * time.Minute
	_, _ = s.pool.Exec(ctx, `UPDATE auth_mail_outbox SET status=$2,available_at=$3,last_error='delivery_failed' WHERE id=$1 AND status='sending' AND attempts=$4`, id, status, time.Now().Add(delay), attempts)
	return true
}
