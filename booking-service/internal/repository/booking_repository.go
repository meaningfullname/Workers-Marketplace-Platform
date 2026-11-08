package repository

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrRequestNotFound = errors.New("request not found")
var ErrBookingNotFound = errors.New("booking not found")

type RequestForBooking struct {
	CustomerProfileID int
	CategoryID        int
	Status            string
}
type BookingDetails struct {
	RequestID       int
	WorkerProfileID int
	Status          string
	FinalPrice      *float64
}

type BookingPaymentData struct {
	Amount float64
}

type BookingUsers struct {
	CustomerUserID int
	WorkerUserID   int
}

type BookingListItem struct {
	BookingID            int        `json:"booking_id"`
	RequestID            int        `json:"request_id"`
	WorkerProfileID      int        `json:"worker_profile_id"`
	CustomerProfileID    int        `json:"customer_profile_id"`
	CategoryID           int        `json:"category_id"`
	CategoryName         string     `json:"category_name"`
	RequestDescription   string     `json:"request_description"`
	Status               string     `json:"status"`
	ScheduledTime        *time.Time `json:"scheduled_time,omitempty"`
	StartTime            *time.Time `json:"start_time,omitempty"`
	EndTime              *time.Time `json:"end_time,omitempty"`
	FinalPrice           *string    `json:"final_price,omitempty"`
	CounterpartyName     string     `json:"counterparty_name"`
	CounterpartyRole     string     `json:"counterparty_role"`
	CounterpartyPhotoURL string     `json:"counterparty_photo_url"`
	Address              string     `json:"address"`
	Latitude             *float64   `json:"latitude,omitempty"`
	Longitude            *float64   `json:"longitude,omitempty"`
	WorkerLatitude       *float64   `json:"worker_latitude,omitempty"`
	WorkerLongitude      *float64   `json:"worker_longitude,omitempty"`
	CompletionEvidence   *string    `json:"completion_evidence,omitempty"`
	CustomerConfirmed    bool       `json:"customer_confirmed"`
	ReviewID             *int       `json:"review_id,omitempty"`
	ReviewRating         *int       `json:"review_rating,omitempty"`
	ReviewComment        *string    `json:"review_comment,omitempty"`
	ReviewPhotoURL       *string    `json:"review_photo_url,omitempty"`
	ReviewCreatedAt      *time.Time `json:"review_created_at,omitempty"`
}

type WorkerReview struct {
	ReviewID        int       `json:"review_id"`
	BookingID       int       `json:"booking_id"`
	WorkerProfileID int       `json:"worker_profile_id"`
	CustomerName    string    `json:"customer_name"`
	CategoryName    string    `json:"category_name"`
	Rating          int       `json:"rating"`
	Comment         string    `json:"comment"`
	PhotoURL        string    `json:"photo_url"`
	CreatedAt       time.Time `json:"created_at"`
}

type WorkerReviewSkill struct {
	WorkerSkillID   int    `json:"worker_skill_id"`
	CategoryName    string `json:"category_name"`
	ExperienceLevel string `json:"experience_level"`
}

type WorkerReviewSummary struct {
	WorkerProfileID int                 `json:"worker_profile_id"`
	WorkerName      string              `json:"worker_name"`
	Bio             string              `json:"bio"`
	ProfilePhotoURL string              `json:"profile_photo_url"`
	AverageRating   float64             `json:"average_rating"`
	ReviewCount     int                 `json:"review_count"`
	Skills          []WorkerReviewSkill `json:"skills"`
	Reviews         []WorkerReview      `json:"reviews"`
}

type BookingRepository struct {
	db *pgxpool.Pool
}

func NewBookingRepository(db *pgxpool.Pool) *BookingRepository {
	return &BookingRepository{db: db}
}

func (r *BookingRepository) EnsureWorkflowColumns(ctx context.Context) error {
	_, err := r.db.Exec(ctx, `
		ALTER TYPE booking_status ADD VALUE IF NOT EXISTS 'awaiting_confirmation';
		ALTER TYPE booking_status ADD VALUE IF NOT EXISTS 'price_pending';
		ALTER TABLE bookings
			ADD COLUMN IF NOT EXISTS completion_evidence text,
			ADD COLUMN IF NOT EXISTS customer_confirmed boolean DEFAULT false,
			ADD COLUMN IF NOT EXISTS created_at timestamp without time zone DEFAULT NOW();
		CREATE TABLE IF NOT EXISTS reviews (
			review_id serial PRIMARY KEY,
			booking_id integer UNIQUE REFERENCES bookings(booking_id) ON DELETE CASCADE,
			customer_profile_id integer REFERENCES customer_profiles(customer_profile_id) ON DELETE SET NULL,
			worker_profile_id integer REFERENCES worker_profiles(worker_profile_id) ON DELETE SET NULL,
			rating integer CHECK (rating >= 1 AND rating <= 5),
			comment text,
			photo_url text,
			created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP
		);
		ALTER TABLE reviews ADD COLUMN IF NOT EXISTS photo_url text;
	`)
	return err
}

// EnsurePerformanceIndexes вызывается отдельно от EnsureWorkflowColumns: новое значение enum
// price_pending нельзя использовать в той же транзакции, где оно добавлено.
func (r *BookingRepository) EnsurePerformanceIndexes(ctx context.Context) error {
	_, err := r.db.Exec(ctx, `
		CREATE INDEX IF NOT EXISTS idx_service_requests_customer_profile
			ON service_requests (customer_profile_id);
		CREATE INDEX IF NOT EXISTS idx_bookings_request
			ON bookings (request_id);
		CREATE INDEX IF NOT EXISTS idx_bookings_price_pending
			ON bookings (created_at) WHERE status = 'price_pending';
		CREATE INDEX IF NOT EXISTS idx_reviews_worker_rating
			ON reviews (worker_profile_id) INCLUDE (rating);
	`)
	return err
}

func (r *BookingRepository) Create(
	ctx context.Context,
	requestID int,
	workerProfileID int,
) (int, error) {

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	var bookingID int
	err = tx.QueryRow(ctx, `
		INSERT INTO bookings (request_id, worker_profile_id, status)
		VALUES ($1, $2, 'price_pending')
		RETURNING booking_id
	`, requestID, workerProfileID).Scan(&bookingID)
	if err != nil {
		return 0, err
	}

	_, err = tx.Exec(ctx, `
		UPDATE service_requests
		SET status = 'accepted'
		WHERE request_id = $1
	`, requestID)
	if err != nil {
		return 0, err
	}

	_, err = tx.Exec(ctx, `
		UPDATE worker_profiles
		SET is_available = false
		WHERE worker_profile_id = $1
	`, workerProfileID)
	if err != nil {
		return 0, err
	}

	return bookingID, tx.Commit(ctx)
}

func (r *BookingRepository) ExpirePendingOffers(ctx context.Context) error {
	_, err := r.db.Exec(ctx, `
		WITH expired AS (
			UPDATE bookings
			SET status = 'cancelled', end_time = NOW()
			WHERE status = 'price_pending'
			  AND COALESCE(created_at, scheduled_time, NOW()) <= NOW() - INTERVAL '15 minutes'
			RETURNING request_id, worker_profile_id
		),
		requests AS (
			UPDATE service_requests sr
			SET status = 'pending'
			FROM expired e
			WHERE sr.request_id = e.request_id
			RETURNING e.worker_profile_id
		)
		UPDATE worker_profiles wp
		SET is_available = true
		WHERE wp.worker_profile_id IN (SELECT worker_profile_id FROM requests)
		  AND NOT EXISTS (
			SELECT 1
			FROM bookings b
			WHERE b.worker_profile_id = wp.worker_profile_id
			  AND b.status IN ('scheduled', 'price_pending', 'in_progress', 'awaiting_confirmation')
		  )
	`)
	return err
}

func (r *BookingRepository) GetRequestForBooking(
	ctx context.Context,
	requestID int,
) (RequestForBooking, error) {
	var req RequestForBooking

	err := r.db.QueryRow(ctx, `
		SELECT customer_profile_id, category_id, status
		FROM service_requests
		WHERE request_id = $1
	`, requestID).Scan(
		&req.CustomerProfileID,
		&req.CategoryID,
		&req.Status,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RequestForBooking{}, ErrRequestNotFound
		}
		return RequestForBooking{}, err
	}

	return req, nil
}

func (r *BookingRepository) IsWorkerEligible(
	ctx context.Context,
	workerProfileID int,
	categoryID int,
) (bool, error) {
	var exists bool

	err := r.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM worker_profiles wp
			JOIN worker_skills ws
			  ON ws.worker_profile_id = wp.worker_profile_id
			WHERE wp.worker_profile_id = $1
			  AND wp.verification_status = 'verified'
			  AND wp.is_available = true
			  AND ws.category_id = $2
			  AND ws.is_verified = true
		)
	`, workerProfileID, categoryID).Scan(&exists)
	if err != nil {
		return false, err
	}

	return exists, nil
}

func (r *BookingRepository) GetBookingDetails(
	ctx context.Context,
	bookingID int,
) (BookingDetails, error) {
	var b BookingDetails
	err := r.db.QueryRow(ctx, `
		SELECT request_id, worker_profile_id, status, final_price::float8
		FROM bookings
		WHERE booking_id = $1
	`, bookingID).Scan(&b.RequestID, &b.WorkerProfileID, &b.Status, &b.FinalPrice)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return BookingDetails{}, ErrBookingNotFound
		}
		return BookingDetails{}, err
	}
	return b, nil
}

func (r *BookingRepository) MarkPriceAccepted(
	ctx context.Context,
	bookingID int,
	requestID int,
) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		UPDATE bookings
		SET status = 'scheduled'
		WHERE booking_id = $1
	`, bookingID)
	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `
		UPDATE service_requests
		SET status = 'accepted'
		WHERE request_id = $1
	`, requestID)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (r *BookingRepository) MarkPriceRejected(
	ctx context.Context,
	bookingID int,
) error {
	tag, err := r.db.Exec(ctx, `
		UPDATE bookings
		SET final_price = NULL,
		    status = 'price_pending'
		WHERE booking_id = $1
	`, bookingID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrBookingNotFound
	}
	return nil
}

func (r *BookingRepository) MarkInProgress(
	ctx context.Context,
	bookingID int,
	requestID int,
) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		UPDATE bookings
		SET status = 'in_progress', start_time = NOW()
		WHERE booking_id = $1
	`, bookingID)
	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `
		UPDATE worker_profiles
		SET is_available = false
		WHERE worker_profile_id = (
			SELECT worker_profile_id
			FROM bookings
			WHERE booking_id = $1
		)
	`, bookingID)
	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `
		UPDATE service_requests
		SET status = 'in_progress'
		WHERE request_id = $1
	`, requestID)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (r *BookingRepository) MarkAwaitingConfirmation(
	ctx context.Context,
	bookingID int,
	requestID int,
	evidence string,
) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		UPDATE bookings
		SET status = 'awaiting_confirmation', completion_evidence = $2
		WHERE booking_id = $1
	`, bookingID, evidence)
	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `
		UPDATE worker_profiles
		SET is_available = false
		WHERE worker_profile_id = (
			SELECT worker_profile_id
			FROM bookings
			WHERE booking_id = $1
		)
	`, bookingID)
	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `
		UPDATE service_requests
		SET status = 'in_progress'
		WHERE request_id = $1
	`, requestID)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (r *BookingRepository) MarkCompletionEvidenceRejected(
	ctx context.Context,
	bookingID int,
	requestID int,
	workerProfileID int,
) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		UPDATE bookings
		SET status = 'in_progress',
		    completion_evidence = NULL,
		    customer_confirmed = false
		WHERE booking_id = $1
	`, bookingID)
	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `
		UPDATE service_requests
		SET status = 'in_progress'
		WHERE request_id = $1
	`, requestID)
	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `
		UPDATE worker_profiles
		SET is_available = false
		WHERE worker_profile_id = $1
	`, workerProfileID)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (r *BookingRepository) MarkRejected(
	ctx context.Context,
	bookingID int,
	requestID int,
	workerProfileID int,
) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		UPDATE bookings
		SET status = 'cancelled', end_time = NOW()
		WHERE booking_id = $1
	`, bookingID)
	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `
		UPDATE service_requests
		SET status = 'pending'
		WHERE request_id = $1
	`, requestID)
	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `
		UPDATE worker_profiles
		SET is_available = false
		WHERE worker_profile_id = $1
	`, workerProfileID)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (r *BookingRepository) MarkCancelledByStaff(
	ctx context.Context,
	bookingID int,
	requestID int,
	workerProfileID int,
) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
		UPDATE bookings
		SET status = 'cancelled',
		    end_time = NOW(),
		    customer_confirmed = false
		WHERE booking_id = $1
		  AND status NOT IN ('completed', 'cancelled')
	`, bookingID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrBookingNotFound
	}

	_, err = tx.Exec(ctx, `
		UPDATE service_requests
		SET status = 'cancelled'
		WHERE request_id = $1
	`, requestID)
	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `
		UPDATE worker_profiles
		SET is_available = false
		WHERE worker_profile_id = $1
	`, workerProfileID)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (r *BookingRepository) MarkCompleted(
	ctx context.Context,
	bookingID int,
	requestID int,
	workerProfileID int,
) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		UPDATE bookings
		SET status = 'completed', end_time = NOW(), customer_confirmed = true
		WHERE booking_id = $1
	`, bookingID)
	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `
		UPDATE service_requests
		SET status = 'completed'
		WHERE request_id = $1
	`, requestID)
	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `
		UPDATE worker_profiles
		SET is_available = false
		WHERE worker_profile_id = $1
	`, workerProfileID)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (r *BookingRepository) GetPaymentData(ctx context.Context, bookingID int) (BookingPaymentData, error) {
	var data BookingPaymentData
	err := r.db.QueryRow(ctx, `
		SELECT COALESCE(b.final_price, 0)::float8
		FROM bookings b
		WHERE b.booking_id = $1
	`, bookingID).Scan(&data.Amount)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return BookingPaymentData{}, ErrBookingNotFound
		}
		return BookingPaymentData{}, err
	}
	return data, nil
}

func (r *BookingRepository) GetBookingUsers(ctx context.Context, bookingID int) (BookingUsers, error) {
	var users BookingUsers
	err := r.db.QueryRow(ctx, `
		SELECT cp.user_id, wp.user_id
		FROM bookings b
		JOIN service_requests sr ON sr.request_id = b.request_id
		JOIN customer_profiles cp ON cp.customer_profile_id = sr.customer_profile_id
		JOIN worker_profiles wp ON wp.worker_profile_id = b.worker_profile_id
		WHERE b.booking_id = $1
	`, bookingID).Scan(&users.CustomerUserID, &users.WorkerUserID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return BookingUsers{}, ErrBookingNotFound
		}
		return BookingUsers{}, err
	}
	return users, nil
}

func (r *BookingRepository) SetFinalPrice(ctx context.Context, bookingID int, amount float64) error {
	tag, err := r.db.Exec(ctx, `
		UPDATE bookings
		SET final_price = $2
		WHERE booking_id = $1
	`, bookingID, amount)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrBookingNotFound
	}
	return nil
}

func (r *BookingRepository) IsRequestAvailable(ctx context.Context, requestID int) (bool, error) {
	var status string

	err := r.db.QueryRow(ctx, `
		SELECT status FROM service_requests
		WHERE request_id = $1
	`, requestID).Scan(&status)

	if err != nil {
		return false, err
	}

	return status == "pending", nil
}

func (r *BookingRepository) ListByCustomerProfile(
	ctx context.Context,
	customerProfileID int,
) ([]BookingListItem, error) {
	rows, err := r.db.Query(ctx, `
		SELECT
			b.booking_id,
			b.request_id,
			b.worker_profile_id,
			sr.customer_profile_id,
			sr.category_id,
			COALESCE(sc.name, '') AS category_name,
			COALESCE(sr.description, '') AS request_description,
			b.status,
			b.scheduled_time,
			b.start_time,
			b.end_time,
			b.final_price::text AS final_price,
			COALESCE(wu.full_name, '') AS counterparty_name,
			'worker' AS counterparty_role,
			COALESCE(wp.profile_photo_url, '') AS counterparty_photo_url,
			COALESCE(sr.address, '') AS address,
			sr.latitude::float8,
			sr.longitude::float8,
			wp.current_latitude::float8,
			wp.current_longitude::float8,
			b.completion_evidence,
			COALESCE(b.customer_confirmed, false),
			r.review_id,
			r.rating,
			r.comment,
			r.photo_url,
			r.created_at
		FROM bookings b
		JOIN service_requests sr ON sr.request_id = b.request_id
		LEFT JOIN service_categories sc ON sc.category_id = sr.category_id
		LEFT JOIN worker_profiles wp ON wp.worker_profile_id = b.worker_profile_id
		LEFT JOIN users wu ON wu.user_id = wp.user_id
		LEFT JOIN reviews r ON r.booking_id = b.booking_id
		WHERE sr.customer_profile_id = $1
		ORDER BY COALESCE(b.scheduled_time, sr.created_at) DESC, b.booking_id DESC
	`, customerProfileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]BookingListItem, 0)
	for rows.Next() {
		var item BookingListItem
		if err := rows.Scan(
			&item.BookingID,
			&item.RequestID,
			&item.WorkerProfileID,
			&item.CustomerProfileID,
			&item.CategoryID,
			&item.CategoryName,
			&item.RequestDescription,
			&item.Status,
			&item.ScheduledTime,
			&item.StartTime,
			&item.EndTime,
			&item.FinalPrice,
			&item.CounterpartyName,
			&item.CounterpartyRole,
			&item.CounterpartyPhotoURL,
			&item.Address,
			&item.Latitude,
			&item.Longitude,
			&item.WorkerLatitude,
			&item.WorkerLongitude,
			&item.CompletionEvidence,
			&item.CustomerConfirmed,
			&item.ReviewID,
			&item.ReviewRating,
			&item.ReviewComment,
			&item.ReviewPhotoURL,
			&item.ReviewCreatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, item)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return items, nil
}

func (r *BookingRepository) ListByWorkerProfile(
	ctx context.Context,
	workerProfileID int,
) ([]BookingListItem, error) {
	rows, err := r.db.Query(ctx, `
		SELECT
			b.booking_id,
			b.request_id,
			b.worker_profile_id,
			sr.customer_profile_id,
			sr.category_id,
			COALESCE(sc.name, '') AS category_name,
			COALESCE(sr.description, '') AS request_description,
			b.status,
			b.scheduled_time,
			b.start_time,
			b.end_time,
			b.final_price::text AS final_price,
			COALESCE(cu.full_name, '') AS counterparty_name,
			'customer' AS counterparty_role,
			COALESCE(cp.profile_photo_url, '') AS counterparty_photo_url,
			COALESCE(sr.address, '') AS address,
			sr.latitude::float8,
			sr.longitude::float8,
			wp.current_latitude::float8,
			wp.current_longitude::float8,
			b.completion_evidence,
			COALESCE(b.customer_confirmed, false),
			r.review_id,
			r.rating,
			r.comment,
			r.photo_url,
			r.created_at
		FROM bookings b
		JOIN service_requests sr ON sr.request_id = b.request_id
		LEFT JOIN service_categories sc ON sc.category_id = sr.category_id
		LEFT JOIN customer_profiles cp ON cp.customer_profile_id = sr.customer_profile_id
		LEFT JOIN users cu ON cu.user_id = cp.user_id
		LEFT JOIN worker_profiles wp ON wp.worker_profile_id = b.worker_profile_id
		LEFT JOIN reviews r ON r.booking_id = b.booking_id
		WHERE b.worker_profile_id = $1
		ORDER BY COALESCE(b.scheduled_time, sr.created_at) DESC, b.booking_id DESC
	`, workerProfileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]BookingListItem, 0)
	for rows.Next() {
		var item BookingListItem
		if err := rows.Scan(
			&item.BookingID,
			&item.RequestID,
			&item.WorkerProfileID,
			&item.CustomerProfileID,
			&item.CategoryID,
			&item.CategoryName,
			&item.RequestDescription,
			&item.Status,
			&item.ScheduledTime,
			&item.StartTime,
			&item.EndTime,
			&item.FinalPrice,
			&item.CounterpartyName,
			&item.CounterpartyRole,
			&item.CounterpartyPhotoURL,
			&item.Address,
			&item.Latitude,
			&item.Longitude,
			&item.WorkerLatitude,
			&item.WorkerLongitude,
			&item.CompletionEvidence,
			&item.CustomerConfirmed,
			&item.ReviewID,
			&item.ReviewRating,
			&item.ReviewComment,
			&item.ReviewPhotoURL,
			&item.ReviewCreatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, item)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return items, nil
}

func (r *BookingRepository) SaveReview(
	ctx context.Context,
	bookingID int,
	customerProfileID int,
	workerProfileID int,
	rating int,
	comment string,
	photoURL string,
) (int, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	var reviewID int
	err = tx.QueryRow(ctx, `
		INSERT INTO reviews (booking_id, customer_profile_id, worker_profile_id, rating, comment, photo_url)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''))
		ON CONFLICT (booking_id) DO UPDATE
		SET rating = EXCLUDED.rating,
		    comment = EXCLUDED.comment,
		    photo_url = COALESCE(EXCLUDED.photo_url, reviews.photo_url),
		    created_at = CURRENT_TIMESTAMP
		RETURNING review_id
	`, bookingID, customerProfileID, workerProfileID, rating, comment, photoURL).Scan(&reviewID)
	if err != nil {
		return 0, err
	}

	_, err = tx.Exec(ctx, `
		UPDATE worker_profiles wp
		SET rating = stats.average_rating
		FROM (
			SELECT worker_profile_id, ROUND(AVG(rating)::numeric, 2) AS average_rating
			FROM reviews
			WHERE worker_profile_id = $1
			GROUP BY worker_profile_id
		) stats
		WHERE wp.worker_profile_id = stats.worker_profile_id
	`, workerProfileID)
	if err != nil {
		return 0, err
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return reviewID, nil
}

func (r *BookingRepository) ListWorkerReviews(ctx context.Context, workerProfileID int) (WorkerReviewSummary, error) {
	summary := WorkerReviewSummary{
		WorkerProfileID: workerProfileID,
		Skills:          make([]WorkerReviewSkill, 0),
		Reviews:         make([]WorkerReview, 0),
	}
	_ = r.db.QueryRow(ctx, `
		SELECT COALESCE(u.full_name, ''), COALESCE(wp.bio, ''), COALESCE(wp.profile_photo_url, ''), COALESCE(wp.rating, 0)::float8
		FROM worker_profiles wp
		LEFT JOIN users u ON u.user_id = wp.user_id
		WHERE wp.worker_profile_id = $1
	`, workerProfileID).Scan(
		&summary.WorkerName,
		&summary.Bio,
		&summary.ProfilePhotoURL,
		&summary.AverageRating,
	)

	skillRows, err := r.db.Query(ctx, `
		SELECT ws.worker_skill_id, COALESCE(sc.name, ''), ws.experience_level
		FROM worker_skills ws
		LEFT JOIN service_categories sc ON sc.category_id = ws.category_id
		WHERE ws.worker_profile_id = $1
		  AND ws.is_verified = true
		ORDER BY sc.name, ws.worker_skill_id
	`, workerProfileID)
	if err != nil {
		return WorkerReviewSummary{}, err
	}
	for skillRows.Next() {
		var skill WorkerReviewSkill
		if err := skillRows.Scan(&skill.WorkerSkillID, &skill.CategoryName, &skill.ExperienceLevel); err != nil {
			skillRows.Close()
			return WorkerReviewSummary{}, err
		}
		summary.Skills = append(summary.Skills, skill)
	}
	if err := skillRows.Err(); err != nil {
		skillRows.Close()
		return WorkerReviewSummary{}, err
	}
	skillRows.Close()

	rows, err := r.db.Query(ctx, `
		SELECT
			r.review_id,
			r.booking_id,
			r.worker_profile_id,
			COALESCE(u.full_name, 'Customer') AS customer_name,
			COALESCE(sc.name, '') AS category_name,
			r.rating,
			COALESCE(r.comment, '') AS comment,
			COALESCE(r.photo_url, '') AS photo_url,
			r.created_at
		FROM reviews r
		LEFT JOIN customer_profiles cp ON cp.customer_profile_id = r.customer_profile_id
		LEFT JOIN users u ON u.user_id = cp.user_id
		LEFT JOIN bookings b ON b.booking_id = r.booking_id
		LEFT JOIN service_requests sr ON sr.request_id = b.request_id
		LEFT JOIN service_categories sc ON sc.category_id = sr.category_id
		WHERE r.worker_profile_id = $1
		ORDER BY r.created_at DESC, r.review_id DESC
	`, workerProfileID)
	if err != nil {
		return WorkerReviewSummary{}, err
	}
	defer rows.Close()

	total := 0
	for rows.Next() {
		var item WorkerReview
		if err := rows.Scan(
			&item.ReviewID,
			&item.BookingID,
			&item.WorkerProfileID,
			&item.CustomerName,
			&item.CategoryName,
			&item.Rating,
			&item.Comment,
			&item.PhotoURL,
			&item.CreatedAt,
		); err != nil {
			return WorkerReviewSummary{}, err
		}
		total += item.Rating
		summary.Reviews = append(summary.Reviews, item)
	}
	if err := rows.Err(); err != nil {
		return WorkerReviewSummary{}, err
	}
	summary.ReviewCount = len(summary.Reviews)
	if summary.ReviewCount > 0 {
		summary.AverageRating = float64(total) / float64(summary.ReviewCount)
	}
	return summary, nil
}
