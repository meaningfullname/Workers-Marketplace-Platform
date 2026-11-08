package repository

import (
	"context"
	"database/sql"
	"diploma/usermanagement-service/internal/model"
	"sync/atomic"

	"github.com/jackc/pgx/v5/pgxpool"
)

type CustomerProfileRepository struct {
	db             *pgxpool.Pool
	columnsEnsured atomic.Bool
}

func NewCustomerProfileRepository(db *pgxpool.Pool) *CustomerProfileRepository {
	return &CustomerProfileRepository{db: db}
}

func (r *CustomerProfileRepository) Create(ctx context.Context, userID int) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO customer_profiles (user_id)
		 VALUES ($1)
		 ON CONFLICT (user_id) DO NOTHING`,
		userID,
	)
	return err
}

// EnsureProfileColumns вызывается при каждом чтении профиля, поэтому схему проверяет только первый
// вызов: ALTER TABLE берёт эксклюзивную блокировку и выстраивает все запросы к таблице в очередь.
func (r *CustomerProfileRepository) EnsureProfileColumns(ctx context.Context) error {
	if r.columnsEnsured.Load() {
		return nil
	}
	_, err := r.db.Exec(ctx, `
		ALTER TABLE customer_profiles
			ADD COLUMN IF NOT EXISTS bio text,
			ADD COLUMN IF NOT EXISTS profile_photo_url text
	`)
	if err == nil {
		r.columnsEnsured.Store(true)
	}
	return err
}

func (r *CustomerProfileRepository) Upsert(
	ctx context.Context,
	userID int,
	address string,
	latitude *float64,
	longitude *float64,
	bio string,
	profilePhotoURL *string,
) (*model.CustomerProfile, error) {
	if err := r.EnsureProfileColumns(ctx); err != nil {
		return nil, err
	}

	var profile model.CustomerProfile
	var scannedAddress sql.NullString
	var scannedLatitude sql.NullFloat64
	var scannedLongitude sql.NullFloat64
	var scannedBio sql.NullString
	var scannedPhoto sql.NullString

	err := r.db.QueryRow(ctx, `
		INSERT INTO customer_profiles
			(user_id, address, latitude, longitude, location, bio, profile_photo_url)
		VALUES
			($1, $2, $3, $4,
			 CASE WHEN $3::numeric IS NOT NULL AND $4::numeric IS NOT NULL
			 THEN ST_SetSRID(ST_MakePoint($4, $3), 4326)::geography ELSE NULL END,
			 $5, $6)
		ON CONFLICT (user_id) DO UPDATE SET
			address = EXCLUDED.address,
			latitude = COALESCE(EXCLUDED.latitude, customer_profiles.latitude),
			longitude = COALESCE(EXCLUDED.longitude, customer_profiles.longitude),
			location = COALESCE(EXCLUDED.location, customer_profiles.location),
			bio = EXCLUDED.bio,
			profile_photo_url = COALESCE(EXCLUDED.profile_photo_url, customer_profiles.profile_photo_url)
		RETURNING customer_profile_id, user_id, address, latitude, longitude, bio, profile_photo_url
	`, userID, address, latitude, longitude, bio, profilePhotoURL).Scan(
		&profile.ID,
		&profile.UserID,
		&scannedAddress,
		&scannedLatitude,
		&scannedLongitude,
		&scannedBio,
		&scannedPhoto,
	)
	if err != nil {
		return nil, err
	}

	applyCustomerProfileFields(&profile, scannedAddress, scannedLatitude, scannedLongitude, scannedBio, scannedPhoto)
	return &profile, nil
}

func (r *CustomerProfileRepository) GetByUserID(
	ctx context.Context,
	userID int,
) (*model.CustomerProfile, error) {
	if err := r.EnsureProfileColumns(ctx); err != nil {
		return nil, err
	}

	var profile model.CustomerProfile
	var address sql.NullString
	var latitude sql.NullFloat64
	var longitude sql.NullFloat64
	var bio sql.NullString
	var photo sql.NullString

	err := r.db.QueryRow(ctx, `
		SELECT customer_profile_id, user_id, address, latitude, longitude, bio, profile_photo_url
		FROM customer_profiles
		WHERE user_id = $1
	`, userID).Scan(
		&profile.ID,
		&profile.UserID,
		&address,
		&latitude,
		&longitude,
		&bio,
		&photo,
	)

	if err != nil {
		return nil, err
	}

	applyCustomerProfileFields(&profile, address, latitude, longitude, bio, photo)

	return &profile, nil
}

func (r *CustomerProfileRepository) UpdateProfilePhoto(
	ctx context.Context,
	userID int,
	profilePhotoURL string,
) (*model.CustomerProfile, error) {
	if err := r.EnsureProfileColumns(ctx); err != nil {
		return nil, err
	}

	var profile model.CustomerProfile
	var address sql.NullString
	var latitude sql.NullFloat64
	var longitude sql.NullFloat64
	var bio sql.NullString
	var photo sql.NullString

	err := r.db.QueryRow(ctx, `
		INSERT INTO customer_profiles (user_id, profile_photo_url)
		VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET
			profile_photo_url = EXCLUDED.profile_photo_url
		RETURNING customer_profile_id, user_id, address, latitude, longitude, bio, profile_photo_url
	`, userID, profilePhotoURL).Scan(
		&profile.ID,
		&profile.UserID,
		&address,
		&latitude,
		&longitude,
		&bio,
		&photo,
	)
	if err != nil {
		return nil, err
	}

	applyCustomerProfileFields(&profile, address, latitude, longitude, bio, photo)
	return &profile, nil
}

func applyCustomerProfileFields(
	profile *model.CustomerProfile,
	address sql.NullString,
	latitude sql.NullFloat64,
	longitude sql.NullFloat64,
	bio sql.NullString,
	photo sql.NullString,
) {
	if address.Valid {
		profile.Address = &address.String
	}
	if latitude.Valid {
		profile.Latitude = &latitude.Float64
	}
	if longitude.Valid {
		profile.Longitude = &longitude.Float64
	}
	if bio.Valid {
		profile.Bio = bio.String
	}
	if photo.Valid {
		profile.ProfilePhotoURL = &photo.String
	}
}
