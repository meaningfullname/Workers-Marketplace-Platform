package repository

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

type NearbyWorker struct {
	WorkerID        int     `json:"worker_id"`
	FullName        string  `json:"full_name"`
	Price           int     `json:"price"`
	ExperienceLevel string  `json:"experience_level"`
	CategoryName    string  `json:"category_name"`
	Latitude        float64 `json:"latitude"`
	Longitude       float64 `json:"longitude"`
	DistanceMeters  float64 `json:"distance_meters"`
	AverageRating   float64 `json:"average_rating"`
	ReviewCount     int     `json:"review_count"`
}

type GeolocationRepository struct {
	db *pgxpool.Pool
}

func NewGeolocationRepository(db *pgxpool.Pool) *GeolocationRepository {
	return &GeolocationRepository{db: db}
}

func (r *GeolocationRepository) FindNearbyWorkers(
	ctx context.Context,
	categoryID int,
	latitude float64,
	longitude float64,
	radiusMeters int,
) ([]NearbyWorker, error) {
	rows, err := r.db.Query(ctx, `
		WITH origin AS (
			SELECT ST_SetSRID(ST_MakePoint($3, $2), 4326)::geography AS point
		)
		SELECT
			wp.worker_profile_id,
			u.full_name,
			COALESCE(ws.price_base, 0)::int,
			ws.experience_level,
			sc.name,
			wp.current_latitude::float8,
			wp.current_longitude::float8,
			ST_Distance(wp.current_location, origin.point)::float8 AS distance_meters,
			COALESCE(review_stats.average_rating, 0)::float8 AS average_rating,
			COALESCE(review_stats.review_count, 0)::int AS review_count
		FROM worker_profiles wp
		JOIN users u ON u.user_id = wp.user_id
		JOIN worker_skills ws ON ws.worker_profile_id = wp.worker_profile_id
		JOIN service_categories sc ON sc.category_id = ws.category_id
		CROSS JOIN origin
		LEFT JOIN LATERAL (
			SELECT ROUND(AVG(r.rating)::numeric, 2) AS average_rating, COUNT(*) AS review_count
			FROM reviews r
			WHERE r.worker_profile_id = wp.worker_profile_id
		) review_stats ON true
		WHERE ws.category_id = $1
		  AND ws.is_verified = true
		  AND u.status = 'active'
		  AND wp.verification_status = 'verified'
		  AND wp.is_available = true
		  AND wp.current_location IS NOT NULL
		  AND ST_DWithin(wp.current_location, origin.point, $4)
		ORDER BY distance_meters ASC, review_stats.average_rating DESC NULLS LAST
	`, categoryID, latitude, longitude, radiusMeters)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	workers := make([]NearbyWorker, 0)
	for rows.Next() {
		var worker NearbyWorker
		if err := rows.Scan(
			&worker.WorkerID,
			&worker.FullName,
			&worker.Price,
			&worker.ExperienceLevel,
			&worker.CategoryName,
			&worker.Latitude,
			&worker.Longitude,
			&worker.DistanceMeters,
			&worker.AverageRating,
			&worker.ReviewCount,
		); err != nil {
			return nil, err
		}
		workers = append(workers, worker)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return workers, nil
}

func (r *GeolocationRepository) UpdateWorkerLocation(
	ctx context.Context,
	userID int,
	latitude float64,
	longitude float64,
) error {
	_, err := r.db.Exec(ctx, `
		UPDATE worker_profiles
		SET
			current_latitude = $2::numeric,
			current_longitude = $3::numeric,
			current_location = ST_SetSRID(ST_MakePoint($3::double precision, $2::double precision), 4326)::geography
		WHERE user_id = $1
	`, userID, latitude, longitude)

	return err
}

func (r *GeolocationRepository) UpdateCustomerLocation(
	ctx context.Context,
	userID int,
	latitude float64,
	longitude float64,
) error {
	_, err := r.db.Exec(ctx, `
		UPDATE customer_profiles
		SET
			latitude = $2::numeric,
			longitude = $3::numeric,
			location = ST_SetSRID(ST_MakePoint($3::double precision, $2::double precision), 4326)::geography
		WHERE user_id = $1
	`, userID, latitude, longitude)

	return err
}
