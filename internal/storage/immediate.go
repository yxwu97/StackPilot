package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

func executeImmediate(ctx context.Context, database *sql.DB, action func(*sql.Conn) error) (err error) {
	connection, err := database.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire immediate transaction connection: %w", err)
	}
	defer func() {
		if closeErr := connection.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close immediate transaction connection: %w", closeErr))
		}
	}()
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("begin immediate transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			if _, rollbackErr := connection.ExecContext(context.WithoutCancel(ctx), "ROLLBACK"); rollbackErr != nil {
				err = errors.Join(err, fmt.Errorf("rollback immediate transaction: %w", rollbackErr))
			}
		}
	}()
	if err := action(connection); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit immediate transaction: %w", err)
	}
	committed = true
	return nil
}
