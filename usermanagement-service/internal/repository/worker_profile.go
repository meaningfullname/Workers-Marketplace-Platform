package repository

import (
	"context"
	"diploma/usermanagement-service/internal/model"
	"sync/atomic"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type WorkerProfileRepository struct {
	db             *pgxpool.Pool
	columnsEnsured atomic.Bool
}

type VerifiedWorkerSkill struct {
	WorkerSkillID   int    `json:"worker_skill_id"`
	CategoryID      int    `json:"category_id"`
	CategoryName    string `json:"category_name"`
	ExperienceLevel string `json:"experience_level"`
	PriceBase       string `json:"price_base"`
}

type WorkerIdentityDocument struct {
	ID              int     `json:"identity_document_id"`
	WorkerProfileID int     `json:"worker_profile_id"`
	FileName        string  `json:"file_name"`
	FilePath        string  `json:"file_path"`
	ContentType     string  `json:"content_type"`
	Status          string  `json:"status"`
	UploadedAt      string  `json:"uploaded_at"`
	ReviewedAt      *string `json:"reviewed_at,omitempty"`
	ReviewerEmail   *string `json:"reviewer_email,omitempty"`
	RejectionReason *string `json:"rejection_reason,omitempty"`
}

type WorkerReadiness struct {
	IdentityVerified bool
	HasVerifiedSkill bool
}

func NewWorkerProfileRepository(db *pgxpool.Pool) *WorkerProfileRepository {
	return &WorkerProfileRepository{db: db}
}

// EnsureProfileColumns вызывается при каждом обращении к профилю, поэтому схему проверяет только
// первый вызов: ALTER TABLE берёт эксклюзивную блокировку и выстраивает запросы к таблице в очередь.
func (r *WorkerProfileRepository) EnsureProfileColumns(ctx context.Context) error {
	if r.columnsEnsured.Load() {
		return nil
	}
	_, err := r.db.Exec(ctx, `
		ALTER TABLE worker_profiles
		ADD COLUMN IF NOT EXISTS profile_photo_url text;

		CREATE TABLE IF NOT EXISTS worker_identity_documents (
			identity_document_id serial PRIMARY KEY,
			worker_profile_id integer NOT NULL REFERENCES worker_profiles(worker_profile_id) ON DELETE CASCADE,
			file_name varchar(255) NOT NULL,
			file_path text NOT NULL,
			content_type varchar(100),
			status varchar(20) NOT NULL DEFAULT 'pending'
				CHECK (status IN ('pending', 'verified', 'rejected', 'replaced')),
			rejection_reason text,
			created_at timestamp with time zone DEFAULT now(),
			reviewed_at timestamp with time zone,
			reviewed_by_user_id integer REFERENCES users(user_id) ON DELETE SET NULL,
			assigned_manager_id integer REFERENCES users(user_id) ON DELETE SET NULL
		);

		ALTER TABLE worker_identity_documents
			ADD COLUMN IF NOT EXISTS assigned_manager_id integer REFERENCES users(user_id) ON DELETE SET NULL;

		ALTER TABLE worker_identity_documents
			DROP CONSTRAINT IF EXISTS worker_identity_documents_status_check;

		ALTER TABLE worker_identity_documents
			ADD CONSTRAINT worker_identity_documents_status_check
			CHECK (status IN ('pending', 'verified', 'rejected', 'replaced'));

		CREATE INDEX IF NOT EXISTS idx_worker_identity_documents_profile_status
			ON worker_identity_documents(worker_profile_id, status, created_at DESC);
	`)
	if err == nil {
		r.columnsEnsured.Store(true)
	}
	return err
}

func (r *WorkerProfileRepository) Create(ctx context.Context, userID int) error {
	if err := r.EnsureProfileColumns(ctx); err != nil {
		return err
	}
	_, err := r.db.Exec(ctx,
		`INSERT INTO worker_profiles (user_id, verification_status)
		 VALUES ($1, 'pending')
		 ON CONFLICT (user_id) DO NOTHING`,
		userID,
	)
	return err
}

func (r *WorkerProfileRepository) UpsertDetails(
	ctx context.Context,
	userID int,
	bio string,
	latitude *float64,
	longitude *float64,
	profilePhotoURL *string,
) (model.WorkerProfile, error) {
	if err := r.EnsureProfileColumns(ctx); err != nil {
		return model.WorkerProfile{}, err
	}

	query := `
		INSERT INTO worker_profiles
			(user_id, bio, current_latitude, current_longitude, profile_photo_url, verification_status)
		VALUES
			($1, $2, $3, $4, $5, 'pending')
		ON CONFLICT (user_id) DO UPDATE
		SET bio = EXCLUDED.bio,
		    current_latitude = COALESCE(EXCLUDED.current_latitude, worker_profiles.current_latitude),
		    current_longitude = COALESCE(EXCLUDED.current_longitude, worker_profiles.current_longitude),
		    profile_photo_url = COALESCE(EXCLUDED.profile_photo_url, worker_profiles.profile_photo_url)
		RETURNING
			worker_profile_id,
			user_id,
			COALESCE(bio, ''),
			COALESCE(rating, 0),
			verification_status,
			is_available,
			current_latitude,
			current_longitude,
			profile_photo_url
	`

	var worker model.WorkerProfile
	err := r.db.QueryRow(ctx, query, userID, bio, latitude, longitude, profilePhotoURL).Scan(
		&worker.ID,
		&worker.UserID,
		&worker.Bio,
		&worker.Rating,
		&worker.VerificationStatus,
		&worker.IsAvailable,
		&worker.CurrentLatitude,
		&worker.CurrentLongitude,
		&worker.ProfilePhotoURL,
	)
	return worker, err
}

func (r *WorkerProfileRepository) UpdateProfilePhoto(
	ctx context.Context,
	userID int,
	profilePhotoURL string,
) (model.WorkerProfile, error) {
	if err := r.EnsureProfileColumns(ctx); err != nil {
		return model.WorkerProfile{}, err
	}

	query := `
		INSERT INTO worker_profiles (user_id, profile_photo_url, verification_status)
		VALUES ($1, $2, 'pending')
		ON CONFLICT (user_id) DO UPDATE SET
			profile_photo_url = EXCLUDED.profile_photo_url
		RETURNING
			worker_profile_id,
			user_id,
			COALESCE(bio, ''),
			COALESCE(rating, 0),
			verification_status,
			is_available,
			current_latitude,
			current_longitude,
			profile_photo_url
	`

	var worker model.WorkerProfile
	err := r.db.QueryRow(ctx, query, userID, profilePhotoURL).Scan(
		&worker.ID,
		&worker.UserID,
		&worker.Bio,
		&worker.Rating,
		&worker.VerificationStatus,
		&worker.IsAvailable,
		&worker.CurrentLatitude,
		&worker.CurrentLongitude,
		&worker.ProfilePhotoURL,
	)
	return worker, err
}
func (r *WorkerProfileRepository) GetByUserID(ctx context.Context, userID int) (int, error) {
	var workerID int

	err := r.db.QueryRow(ctx,
		`SELECT worker_profile_id FROM worker_profiles WHERE user_id=$1`,
		userID,
	).Scan(&workerID)

	return workerID, err
}
func (r *WorkerProfileRepository) UpdateAvailability(ctx context.Context, workerID int, available bool) error {
	_, err := r.db.Exec(ctx,
		`UPDATE worker_profiles SET is_available=$1 WHERE worker_profile_id=$2`,
		available,
		workerID,
	)
	return err
}

func (r *WorkerProfileRepository) GetReadiness(ctx context.Context, workerID int) (WorkerReadiness, error) {
	if err := r.EnsureProfileColumns(ctx); err != nil {
		return WorkerReadiness{}, err
	}

	var result WorkerReadiness
	err := r.db.QueryRow(ctx, `
		SELECT
			EXISTS (
				SELECT 1
				FROM worker_identity_documents
				WHERE worker_profile_id = $1
				  AND status = 'verified'
			) AS identity_verified,
			EXISTS (
				SELECT 1
				FROM worker_skills
				WHERE worker_profile_id = $1
				  AND is_verified = true
			) AS has_verified_skill
	`, workerID).Scan(&result.IdentityVerified, &result.HasVerifiedSkill)
	return result, err
}

func (r *WorkerProfileRepository) HasInProgressBooking(ctx context.Context, workerID int) (bool, error) {
	var exists bool
	err := r.db.QueryRow(ctx,
		`SELECT EXISTS(
			SELECT 1
			FROM bookings
			WHERE worker_profile_id = $1
			  AND status = 'in_progress'
		)`,
		workerID,
	).Scan(&exists)
	return exists, err
}

func (r *WorkerProfileRepository) GetByUserIDFull(ctx context.Context, userID int) (model.WorkerProfile, error) {
	if err := r.EnsureProfileColumns(ctx); err != nil {
		return model.WorkerProfile{}, err
	}
	const query = `
		SELECT
			worker_profile_id,
			user_id,
			COALESCE(bio, ''),
			COALESCE(rating, 0),
			verification_status,
			is_available,
			current_latitude,
			current_longitude,
			profile_photo_url
		FROM worker_profiles
		WHERE user_id = $1
	`

	var worker model.WorkerProfile

	err := r.db.QueryRow(ctx, query, userID).Scan(
		&worker.ID,
		&worker.UserID,
		&worker.Bio,
		&worker.Rating,
		&worker.VerificationStatus,
		&worker.IsAvailable,
		&worker.CurrentLatitude,
		&worker.CurrentLongitude,
		&worker.ProfilePhotoURL,
	)

	return worker, err
}

func (r *WorkerProfileRepository) AddIdentityDocument(
	ctx context.Context,
	userID int,
	fileName string,
	filePath string,
	contentType string,
) (WorkerIdentityDocument, error) {
	if err := r.EnsureProfileColumns(ctx); err != nil {
		return WorkerIdentityDocument{}, err
	}

	workerID, err := r.GetByUserID(ctx, userID)
	if err != nil {
		return WorkerIdentityDocument{}, err
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return WorkerIdentityDocument{}, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		UPDATE worker_identity_documents
		SET status = 'replaced'
		WHERE worker_profile_id = $1
		  AND status = 'pending'
	`, workerID); err != nil {
		return WorkerIdentityDocument{}, err
	}

	var doc WorkerIdentityDocument
	err = tx.QueryRow(ctx, `
		INSERT INTO worker_identity_documents
			(worker_profile_id, file_name, file_path, content_type, status)
		VALUES ($1, $2, $3, $4, 'pending')
		RETURNING identity_document_id, worker_profile_id, file_name, file_path,
			COALESCE(content_type, ''), status, created_at::text
	`, workerID, fileName, filePath, contentType).Scan(
		&doc.ID,
		&doc.WorkerProfileID,
		&doc.FileName,
		&doc.FilePath,
		&doc.ContentType,
		&doc.Status,
		&doc.UploadedAt,
	)
	if err != nil {
		return WorkerIdentityDocument{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return WorkerIdentityDocument{}, err
	}
	_, _ = r.db.Exec(ctx, `
		INSERT INTO notifications (user_id, type, title, message, action_type, action_ref, action_label)
		SELECT user_id, 'identity_document_created', 'New ID document',
			'Worker identity document #' || $1::integer::text || ' needs review.',
			'verify', $1::integer::text, 'Open queue'
		FROM users
		WHERE role IN ('admin', 'manager')
		  AND status = 'active'
	`, doc.ID)
	if err := r.RecalculateVerificationStatus(ctx, workerID); err != nil {
		return WorkerIdentityDocument{}, err
	}
	return doc, nil
}

func (r *WorkerProfileRepository) GetLatestIdentityDocumentByUserID(ctx context.Context, userID int) (*WorkerIdentityDocument, error) {
	if err := r.EnsureProfileColumns(ctx); err != nil {
		return nil, err
	}
	workerID, err := r.GetByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	return r.GetLatestIdentityDocument(ctx, workerID)
}

func (r *WorkerProfileRepository) GetLatestIdentityDocument(ctx context.Context, workerID int) (*WorkerIdentityDocument, error) {
	if err := r.EnsureProfileColumns(ctx); err != nil {
		return nil, err
	}
	var doc WorkerIdentityDocument
	err := r.db.QueryRow(ctx, `
		SELECT wid.identity_document_id, wid.worker_profile_id, wid.file_name, wid.file_path,
			COALESCE(wid.content_type, ''), wid.status, wid.created_at::text,
			wid.reviewed_at::text, u.email, wid.rejection_reason
		FROM worker_identity_documents wid
		LEFT JOIN users u ON u.user_id = wid.reviewed_by_user_id
		WHERE wid.worker_profile_id = $1
		ORDER BY wid.created_at DESC, wid.identity_document_id DESC
		LIMIT 1
	`, workerID).Scan(
		&doc.ID,
		&doc.WorkerProfileID,
		&doc.FileName,
		&doc.FilePath,
		&doc.ContentType,
		&doc.Status,
		&doc.UploadedAt,
		&doc.ReviewedAt,
		&doc.ReviewerEmail,
		&doc.RejectionReason,
	)
	if err != nil {
		return nil, err
	}
	return &doc, nil
}

func (r *WorkerProfileRepository) VerifyIdentityDocument(ctx context.Context, documentID int, reviewerUserID int) error {
	if err := r.EnsureProfileColumns(ctx); err != nil {
		return err
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var workerID int
	err = tx.QueryRow(ctx, `
		UPDATE worker_identity_documents
		SET status = 'verified',
			reviewed_at = now(),
			reviewed_by_user_id = $2,
			rejection_reason = NULL
		WHERE identity_document_id = $1
		RETURNING worker_profile_id
	`, documentID, reviewerUserID).Scan(&workerID)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE worker_identity_documents
		SET status = 'replaced'
		WHERE worker_profile_id = $1
		  AND identity_document_id <> $2
		  AND status = 'verified'
	`, workerID, documentID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	_, _ = r.db.Exec(ctx, `
		INSERT INTO notifications (user_id, type, title, message, action_type, action_ref, action_label)
		SELECT wp.user_id, 'identity_document_verified', 'Identity verified',
			'Your identity document was approved.',
			'profile', '', 'Open profile'
		FROM worker_profiles wp
		WHERE wp.worker_profile_id = $1
	`, workerID)
	return r.RecalculateVerificationStatus(ctx, workerID)
}

func (r *WorkerProfileRepository) RejectIdentityDocument(ctx context.Context, documentID int, reviewerUserID int, reason string) error {
	if err := r.EnsureProfileColumns(ctx); err != nil {
		return err
	}
	var workerID int
	err := r.db.QueryRow(ctx, `
		UPDATE worker_identity_documents
		SET status = 'rejected',
			reviewed_at = now(),
			reviewed_by_user_id = $2,
			rejection_reason = $3
		WHERE identity_document_id = $1
		RETURNING worker_profile_id
	`, documentID, reviewerUserID, reason).Scan(&workerID)
	if err != nil {
		return err
	}
	_, _ = r.db.Exec(ctx, `
		INSERT INTO notifications (user_id, type, title, message, action_type, action_ref, action_label)
		SELECT wp.user_id, 'identity_document_rejected', 'Identity document rejected',
			COALESCE(NULLIF($2, ''), 'Upload a correct ID document and send it again.'),
			'profile', '', 'Open profile'
		FROM worker_profiles wp
		WHERE wp.worker_profile_id = $1
	`, workerID, reason)
	return r.RecalculateVerificationStatus(ctx, workerID)
}

func (r *WorkerProfileRepository) AssignIdentityDocument(ctx context.Context, documentID int, managerUserID int) error {
	if err := r.EnsureProfileColumns(ctx); err != nil {
		return err
	}
	tag, err := r.db.Exec(ctx, `
		UPDATE worker_identity_documents
		SET assigned_manager_id = $2
		WHERE identity_document_id = $1
		  AND status = 'pending'
		  AND (assigned_manager_id IS NULL OR assigned_manager_id = $2)
	`, documentID, managerUserID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (r *WorkerProfileRepository) RecalculateVerificationStatus(ctx context.Context, workerID int) error {
	if err := r.EnsureProfileColumns(ctx); err != nil {
		return err
	}
	_, err := r.db.Exec(ctx, `
		UPDATE worker_profiles wp
		SET verification_status = CASE
			WHEN EXISTS (
				SELECT 1
				FROM worker_identity_documents wid
				WHERE wid.worker_profile_id = wp.worker_profile_id
				  AND wid.status = 'verified'
			)
			AND EXISTS (
				SELECT 1
				FROM worker_skills ws
				WHERE ws.worker_profile_id = wp.worker_profile_id
				  AND ws.is_verified = true
			)
			THEN 'verified'::verification_status
			ELSE 'pending'::verification_status
		END,
		is_available = CASE
			WHEN EXISTS (
				SELECT 1
				FROM worker_identity_documents wid
				WHERE wid.worker_profile_id = wp.worker_profile_id
				  AND wid.status = 'verified'
			)
			AND EXISTS (
				SELECT 1
				FROM worker_skills ws
				WHERE ws.worker_profile_id = wp.worker_profile_id
				  AND ws.is_verified = true
			)
			THEN wp.is_available
			ELSE false
		END
		WHERE wp.worker_profile_id = $1
	`, workerID)
	return err
}

func (r *WorkerProfileRepository) ListVerifiedSkills(ctx context.Context, workerProfileID int) ([]VerifiedWorkerSkill, error) {
	rows, err := r.db.Query(ctx, `
		SELECT
			ws.worker_skill_id,
			ws.category_id,
			COALESCE(sc.name, '') AS category_name,
			ws.experience_level,
			ws.price_base::text
		FROM worker_skills ws
		JOIN service_categories sc ON sc.category_id = ws.category_id
		WHERE ws.worker_profile_id = $1
		  AND ws.is_verified = true
		ORDER BY sc.name, ws.worker_skill_id
	`, workerProfileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]VerifiedWorkerSkill, 0)
	for rows.Next() {
		var item VerifiedWorkerSkill
		if err := rows.Scan(
			&item.WorkerSkillID,
			&item.CategoryID,
			&item.CategoryName,
			&item.ExperienceLevel,
			&item.PriceBase,
		); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
func (r *WorkerProfileRepository) Verify(ctx context.Context, workerID int) error {
	_, err := r.db.Exec(ctx,
		`UPDATE worker_profiles 
		 SET verification_status='verified'
		 WHERE worker_profile_id=$1`,
		workerID,
	)
	return err
}
