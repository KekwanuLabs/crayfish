package security

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"log/slog"
	"strconv"
	"time"
)

const (
	// OTPLength is the length of the OTP in digits
	OTPLength = 6
	// OTPExpiry is the duration for which an OTP is valid
	OTPExpiry = 5 * time.Minute
	// MaxFailedAttempts is the maximum number of failed pairing attempts allowed per hour
	MaxFailedAttempts = 3
)

// PairingService manages OTP-based trust promotion.
type PairingService struct {
	db     *sql.DB
	store  *SessionStore
	logger *slog.Logger
}

// NewPairingService creates a new PairingService instance.
func NewPairingService(db *sql.DB, store *SessionStore, logger *slog.Logger) *PairingService {
	return &PairingService{
		db:     db,
		store:  store,
		logger: logger,
	}
}

// GenerateOTP creates a new 6-digit OTP for the given operator session.
// Returns the OTP string and any error.
func (p *PairingService) GenerateOTP(ctx context.Context, operatorSessionID string) (string, error) {
	otp, err := generateSecureOTP(OTPLength)
	if err != nil {
		p.logger.ErrorContext(ctx, "failed to generate OTP", "error", err)
		return "", fmt.Errorf("failed to generate OTP: %w", err)
	}

	expiresAt := time.Now().Add(OTPExpiry)

	query := `
		INSERT INTO pairing_otps (otp, operator_session_id, created_at, expires_at)
		VALUES (?, ?, datetime('now'), ?)
	`
	_, err = p.db.ExecContext(ctx, query, otp, operatorSessionID, expiresAt.Format(time.RFC3339))
	if err != nil {
		p.logger.ErrorContext(ctx, "failed to store OTP", "error", err, "operator_session_id", operatorSessionID)
		return "", fmt.Errorf("failed to store OTP: %w", err)
	}

	p.logger.InfoContext(ctx, "OTP generated successfully", "operator_session_id", operatorSessionID)
	return otp, nil
}

// RedeemOTP attempts to redeem an OTP and promote the session to TierOperator.
// Returns nil on success, error on failure (wrong OTP, expired, rate limited, etc.).
func (p *PairingService) RedeemOTP(ctx context.Context, sessionID string, otp string) error {
	// Check rate limiting on failed attempts
	if err := p.checkRateLimit(ctx, sessionID); err != nil {
		p.logger.WarnContext(ctx, "rate limit exceeded for session", "session_id", sessionID, "error", err)
		return fmt.Errorf("too many failed attempts: %w", err)
	}

	// Find matching OTP
	var otpRecord struct {
		ID                string
		OTP               string
		OperatorSessionID string
		CreatedAt         string
		ExpiresAt         string
		RedeemedBy        sql.NullString
		RedeemedAt        sql.NullString
	}

	query := `
		SELECT id, otp, operator_session_id, created_at, expires_at, redeemed_by, redeemed_at
		FROM pairing_otps
		WHERE otp = ?
		LIMIT 1
	`
	err := p.db.QueryRowContext(ctx, query, otp).Scan(
		&otpRecord.ID,
		&otpRecord.OTP,
		&otpRecord.OperatorSessionID,
		&otpRecord.CreatedAt,
		&otpRecord.ExpiresAt,
		&otpRecord.RedeemedBy,
		&otpRecord.RedeemedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			p.logger.WarnContext(ctx, "OTP not found", "session_id", sessionID)
			p.recordFailedAttempt(ctx, sessionID)
			return fmt.Errorf("invalid OTP")
		}
		p.logger.ErrorContext(ctx, "failed to query OTP", "error", err, "session_id", sessionID)
		p.recordFailedAttempt(ctx, sessionID)
		return fmt.Errorf("failed to verify OTP: %w", err)
	}

	// Check if OTP has already been redeemed
	if otpRecord.RedeemedBy.Valid {
		p.logger.WarnContext(ctx, "OTP already redeemed", "session_id", sessionID, "redeemed_by", otpRecord.RedeemedBy.String)
		p.recordFailedAttempt(ctx, sessionID)
		return fmt.Errorf("OTP already used")
	}

	// Check if OTP has expired
	expiresAt, err := time.Parse(time.RFC3339, otpRecord.ExpiresAt)
	if err != nil {
		p.logger.ErrorContext(ctx, "failed to parse expiry time", "error", err, "expires_at", otpRecord.ExpiresAt)
		p.recordFailedAttempt(ctx, sessionID)
		return fmt.Errorf("failed to verify OTP validity: %w", err)
	}

	if time.Now().After(expiresAt) {
		p.logger.WarnContext(ctx, "OTP expired", "session_id", sessionID, "expires_at", otpRecord.ExpiresAt)
		p.recordFailedAttempt(ctx, sessionID)
		return fmt.Errorf("OTP has expired")
	}

	// Mark OTP as redeemed
	updateQuery := `
		UPDATE pairing_otps
		SET redeemed_by = ?, redeemed_at = datetime('now')
		WHERE id = ?
	`
	_, err = p.db.ExecContext(ctx, updateQuery, sessionID, otpRecord.ID)
	if err != nil {
		p.logger.ErrorContext(ctx, "failed to mark OTP as redeemed", "error", err, "session_id", sessionID)
		p.recordFailedAttempt(ctx, sessionID)
		return fmt.Errorf("failed to redeem OTP: %w", err)
	}

	// Promote the session to TierOperator
	if err := p.store.SetTrust(ctx, sessionID, TierOperator); err != nil {
		p.logger.ErrorContext(ctx, "failed to promote session trust", "error", err, "session_id", sessionID)
		return fmt.Errorf("failed to promote session: %w", err)
	}

	p.logger.InfoContext(ctx, "OTP redeemed successfully and session promoted to operator", "session_id", sessionID, "operator_session_id", otpRecord.OperatorSessionID)
	p.recordSuccessfulAttempt(ctx, sessionID)
	return nil
}

// CleanExpired removes expired OTPs from the database.
func (p *PairingService) CleanExpired(ctx context.Context) error {
	query := `
		DELETE FROM pairing_otps
		WHERE expires_at < datetime('now')
	`
	result, err := p.db.ExecContext(ctx, query)
	if err != nil {
		p.logger.ErrorContext(ctx, "failed to clean expired OTPs", "error", err)
		return fmt.Errorf("failed to clean expired OTPs: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		p.logger.ErrorContext(ctx, "failed to get rows affected count", "error", err)
		return fmt.Errorf("failed to get cleanup results: %w", err)
	}

	if rowsAffected > 0 {
		p.logger.InfoContext(ctx, "cleaned expired OTPs", "count", rowsAffected)
	}
	return nil
}

// checkRateLimit checks if the session has exceeded the maximum failed attempts within the attempt window.
func (p *PairingService) checkRateLimit(ctx context.Context, sessionID string) error {
	query := `
		SELECT COUNT(*) as attempt_count
		FROM pairing_attempts
		WHERE session_id = ? AND success = 0 AND attempted_at > datetime('now', '-1 hour')
	`
	var attemptCount int
	err := p.db.QueryRowContext(ctx, query, sessionID).Scan(&attemptCount)
	if err != nil {
		p.logger.ErrorContext(ctx, "failed to check rate limit", "error", err, "session_id", sessionID)
		return fmt.Errorf("failed to check rate limit: %w", err)
	}

	if attemptCount >= MaxFailedAttempts {
		return fmt.Errorf("exceeded maximum failed attempts (%d) in the last hour", MaxFailedAttempts)
	}
	return nil
}

// recordFailedAttempt records a failed pairing attempt for the session.
func (p *PairingService) recordFailedAttempt(ctx context.Context, sessionID string) {
	query := `
		INSERT INTO pairing_attempts (session_id, attempted_at, success)
		VALUES (?, datetime('now'), 0)
	`
	_, err := p.db.ExecContext(ctx, query, sessionID)
	if err != nil {
		p.logger.ErrorContext(ctx, "failed to record failed attempt", "error", err, "session_id", sessionID)
	}
}

// recordSuccessfulAttempt records a successful pairing attempt for the session.
func (p *PairingService) recordSuccessfulAttempt(ctx context.Context, sessionID string) {
	query := `
		INSERT INTO pairing_attempts (session_id, attempted_at, success)
		VALUES (?, datetime('now'), 1)
	`
	_, err := p.db.ExecContext(ctx, query, sessionID)
	if err != nil {
		p.logger.ErrorContext(ctx, "failed to record successful attempt", "error", err, "session_id", sessionID)
	}
}

// generateSecureOTP generates a cryptographically secure random OTP of the specified length.
func generateSecureOTP(length int) (string, error) {
	// Generate random bytes
	bytes := make([]byte, length)
	_, err := rand.Read(bytes)
	if err != nil {
		return "", fmt.Errorf("failed to read from random source: %w", err)
	}

	// Convert bytes to digits (0-9)
	otp := ""
	for _, b := range bytes {
		otp += strconv.Itoa(int(b % 10))
	}

	return otp, nil
}
