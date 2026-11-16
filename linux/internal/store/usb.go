package store

import (
	"context"
	"database/sql"
	"errors"
)

// USBBindings maps camera IDs to opaque instances in the local capture broker.
func (s *Store) USBBindings(ctx context.Context) (map[string]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT camera_id, instance FROM usb_cameras`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]string{}
	for rows.Next() {
		var id, instance string
		if err := rows.Scan(&id, &instance); err != nil {
			return nil, err
		}
		result[id] = instance
	}
	return result, rows.Err()
}

func (s *Store) USBInstance(ctx context.Context, id string) (string, error) {
	var instance string
	err := s.DB.QueryRowContext(ctx, `SELECT instance FROM usb_cameras WHERE camera_id=?`, id).Scan(&instance)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return instance, err
}

func (s *Store) BindUSB(ctx context.Context, id, instance string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO usb_cameras(camera_id, instance) VALUES(?, ?)`, id, instance)
	return err
}
