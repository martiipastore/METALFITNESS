// D-98 — bajas desde members contra PostgreSQL real (testcontainers): RN-04,
// RN-34, CL-12, CL-19, regla 7 (deduplicación en la misma transacción) y regla 8
// (el cupo liberado queda en la base).
package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/GuadiViti/Arquitectura-de-Software/services/booking-service/internal/domain"
)

func todas(reservas []domain.Reserva) ([]string, error) {
	ids := make([]string, 0, len(reservas))
	for _, r := range reservas {
		ids = append(ids, r.ID)
	}
	return ids, nil
}

func ocupacion(t *testing.T, db *DB, claseID string) int {
	t.Helper()
	var n int
	require.NoError(t, db.pool.QueryRow(context.Background(), `SELECT ocupacion FROM clases WHERE id=$1`, claseID).Scan(&n))
	return n
}

func TestRevocacionesPostgres(t *testing.T) {
	db := nuevaBase(t)
	ctx := context.Background()
	zumba := actividadPorNombre(t, db, "Zumba")
	funcional := actividadPorNombre(t, db, "Funcional")
	base := time.Now().In(domain.Zona).AddDate(0, 0, 2)
	at := func(dias, h int) time.Time {
		d := base.AddDate(0, 0, dias)
		return time.Date(d.Year(), d.Month(), d.Day(), h, 0, 0, 0, domain.Zona)
	}
	profe := uuidN(500)
	a := clase(t, db, zumba, profe, at(0, 10), 60, 20)
	b := clase(t, db, funcional, profe, at(1, 10), 60, 20)

	t.Run("RN-04: cancela solo lo que elegir devuelve, libera el cupo y deduplica", func(t *testing.T) {
		alumno := uuidN(601)
		ra, _, err := reservar(db, alumno, "rev-a-0001", a.ID)
		require.NoError(t, err)
		rb, _, err := reservar(db, alumno, "rev-b-0001", b.ID)
		require.NoError(t, err)
		ocupA, ocupB := ocupacion(t, db, a.ID), ocupacion(t, db, b.ID)

		rev := domain.Revocacion{EventoID: uuidN(701), Tipo: domain.RevocacionMembresiaCancelada, AlumnoID: alumno, MembresiaID: uuidN(801), OcurridaEn: time.Now()}
		var vistas []domain.Reserva
		n, repetido, err := db.Revocar(ctx, rev, time.Now(), func(rs []domain.Reserva) ([]string, error) {
			vistas = rs
			return []string{rb.ID}, nil // CL-12: la fecha de a sigue cubierta
		})
		require.NoError(t, err)
		require.False(t, repetido)
		require.Equal(t, 1, n)
		require.Len(t, vistas, 2, "elegir recibe las reservas futuras confirmadas del alumno")
		require.Equal(t, ocupA, ocupacion(t, db, a.ID))
		require.Equal(t, ocupB-1, ocupacion(t, db, b.ID), "el cupo se libera una vez")

		var estado, motivo string
		require.NoError(t, db.pool.QueryRow(ctx, `SELECT estado, motivo_cancelacion FROM reservas WHERE id=$1`, rb.ID).Scan(&estado, &motivo))
		require.Equal(t, domain.ReservaCancelada, estado)
		require.Equal(t, domain.MotivoMembresiaCancelada, motivo)
		require.NoError(t, db.pool.QueryRow(ctx, `SELECT estado FROM reservas WHERE id=$1`, ra.ID).Scan(&estado))
		require.Equal(t, domain.ReservaConfirmada, estado)

		llamado := false
		n, repetido, err = db.Revocar(ctx, rev, time.Now(), func([]domain.Reserva) ([]string, error) { llamado = true; return nil, nil })
		require.NoError(t, err)
		require.True(t, repetido, "regla 7: la reentrega del mismo event_id no repite efectos")
		require.False(t, llamado)
		require.Zero(t, n)
		require.Equal(t, ocupB-1, ocupacion(t, db, b.ID))
	})

	t.Run("una falla al decidir no deja rastro y el evento se puede reintentar", func(t *testing.T) {
		alumno := uuidN(602)
		_, _, err := reservar(db, alumno, "rev-a-0002", a.ID)
		require.NoError(t, err)
		rev := domain.Revocacion{EventoID: uuidN(702), Tipo: domain.RevocacionUsuarioDesactivado, AlumnoID: alumno, OcurridaEn: time.Now()}
		_, _, err = db.Revocar(ctx, rev, time.Now(), func([]domain.Reserva) ([]string, error) { return nil, errors.New("members caído") })
		require.Error(t, err)
		var consumidos int
		require.NoError(t, db.pool.QueryRow(ctx, `SELECT count(*) FROM eventos_consumidos WHERE event_id=$1`, rev.EventoID).Scan(&consumidos))
		require.Zero(t, consumidos, "rollback: sin deduplicación ni revocación")
		n, repetido, err := db.Revocar(ctx, rev, time.Now(), todas)
		require.NoError(t, err)
		require.False(t, repetido)
		require.Equal(t, 1, n, "RN-34: el reintento cancela la reserva")
	})

	t.Run("CL-19: una reserva validada antes de la revocación se rechaza; una posterior no", func(t *testing.T) {
		alumno := uuidN(603)
		validadoEn := time.Now()
		rev := domain.Revocacion{EventoID: uuidN(703), Tipo: domain.RevocacionMembresiaCancelada, AlumnoID: alumno, MembresiaID: uuidN(803), OcurridaEn: time.Now()}
		_, _, err := db.Revocar(ctx, rev, time.Now(), todas)
		require.NoError(t, err)
		_, _, err = db.Reservar(ctx, op(alumno, "rev-cl19-01"), a.ID, validadoEn, validarAhora)
		require.ErrorIs(t, err, domain.ErrMembresiaNoVigente, "la vigencia que confirmó members ya no vale")

		// Fuera de la ventana, la vigencia se confirmó después de la revocación:
		// members ya respondía sin la membresía cancelada.
		_, _, err = db.Reservar(ctx, op(alumno, "rev-cl19-02"), a.ID, time.Now().Add(domain.VentanaRevocacion+time.Second), validarAhora)
		require.NoError(t, err)

		baja := domain.Revocacion{EventoID: uuidN(704), Tipo: domain.RevocacionUsuarioDesactivado, AlumnoID: uuidN(604), OcurridaEn: time.Now()}
		_, _, err = db.Revocar(ctx, baja, time.Now(), todas)
		require.NoError(t, err)
		_, _, err = db.Reservar(ctx, op(uuidN(604), "rev-cl19-03"), a.ID, time.Now(), validarAhora)
		require.ErrorIs(t, err, domain.ErrUsuarioInactivo)
	})

	t.Run("CL-19 concurrente: ninguna reserva queda confirmada después de la revocación", func(t *testing.T) {
		// Cada alumno valida su vigencia, luego compiten su reserva y la revocación.
		for i := 0; i < 20; i++ {
			alumno := uuidN(900 + i)
			validadoEn := time.Now()
			rev := domain.Revocacion{EventoID: uuidN(1900 + i), Tipo: domain.RevocacionMembresiaCancelada, AlumnoID: alumno, MembresiaID: uuidN(2900 + i), OcurridaEn: time.Now()}
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				_, _, _ = db.Reservar(ctx, op(alumno, "rev-carrera-"+alumno[len(alumno)-4:]), b.ID, validadoEn, validarAhora)
			}()
			go func() {
				defer wg.Done()
				_, _, err := db.Revocar(ctx, rev, time.Now(), todas)
				require.NoError(t, err)
			}()
			wg.Wait()
			var confirmadas int
			require.NoError(t, db.pool.QueryRow(ctx, `SELECT count(*) FROM reservas WHERE alumno_id=$1 AND estado='CONFIRMADA'`, alumno).Scan(&confirmadas))
			require.Zero(t, confirmadas, "intento %d: la reserva o llegó antes y se canceló, o llegó después y se rechazó", i)
		}
		var ocup, confirmadas int
		require.NoError(t, db.pool.QueryRow(ctx, `SELECT ocupacion, (SELECT count(*) FROM reservas WHERE clase_id=$1 AND estado='CONFIRMADA') FROM clases WHERE id=$1`, b.ID).Scan(&ocup, &confirmadas))
		require.Equal(t, confirmadas, ocup, "regla 8: la ocupación coincide con las reservas confirmadas")
	})

	t.Run("las revocaciones son inmutables", func(t *testing.T) {
		_, err := db.pool.Exec(ctx, `DELETE FROM revocaciones`)
		require.Error(t, err)
	})
}
