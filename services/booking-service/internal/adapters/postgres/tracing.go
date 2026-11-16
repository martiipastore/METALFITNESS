package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// infra devuelve el error solo si es una falla de infraestructura (conexión,
// timeout, error de PostgreSQL sin traducir). Un rechazo de negocio (SIN_CUPO,
// RESERVA_DUPLICADA, una clave en curso) es una respuesta correcta y no se
// marca como error en la traza (D11).
func infra(err error) error {
	var pg *pgconn.PgError
	var conexion *pgconn.ConnectError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &pg), errors.As(err, &conexion), errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, context.Canceled), errors.Is(err, pgx.ErrTxClosed), pgconn.Timeout(err):
		return err
	default:
		return nil
	}
}
