// CA-06.5 y D-105 con PostgreSQL real: la capacidad de una clase nunca supera
// el máximo de su actividad. Bajar el tope ajusta en la misma transacción los
// horarios y las clases futuras programadas, y publica el cambio por el
// outbox; pero se rechaza si una clase futura ya tiene más reservas.
package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/GuadiViti/Arquitectura-de-Software/services/booking-service/internal/domain"
)

func TestCA06_5BajarTopeAjustaHorariosYClases(t *testing.T) {
	db := nuevaBase(t)
	ctx := context.Background()
	gap := actividadPorNombre(t, db, "GAP")
	profesor := uuidN(960)

	h, err := db.CrearHorario(ctx, uuidN(1), domain.Horario{ActividadID: gap.ID, DiaSemana: 3, InicioMinutos: 9 * 60, DuracionMinutos: 60, ProfesorID: profesor, ProfesorNombre: "P", Capacidad: 20})
	require.NoError(t, err)
	c := clase(t, db, gap, profesor, time.Now().Add(72*time.Hour).Truncate(time.Minute), 60, 20)
	for i, alumno := range []string{uuidN(961), uuidN(962)} {
		_, _, err = reservar(db, alumno, "tope-reserva-"+string(rune('a'+i)), c.ID)
		require.NoError(t, err)
	}

	// Lo mismo que hace el caso de uso ModificarActividad con el tope nuevo.
	tope := func(n int) func(domain.Actividad, domain.ClasesFuturas) (domain.Actividad, error) {
		return func(a domain.Actividad, f domain.ClasesFuturas) (domain.Actividad, error) {
			a.CapacidadMaxima = n
			if err := a.ValidarTope(f); err != nil {
				return a, err
			}
			return a, a.Validar()
		}
	}
	capacidades := func() (horario, clase int) {
		require.NoError(t, db.pool.QueryRow(ctx, `SELECT capacidad FROM horarios WHERE id=$1`, h.ID).Scan(&horario))
		require.NoError(t, db.pool.QueryRow(ctx, `SELECT capacidad FROM clases WHERE id=$1`, c.ID).Scan(&clase))
		return
	}

	t.Run("por_debajo_de_las_reservas_se_rechaza_sin_cambios", func(t *testing.T) {
		_, err := db.ModificarActividad(ctx, uuidN(1), gap.ID, tope(1))
		require.ErrorIs(t, err, domain.ErrTopeMenorQueReservas)
		hc, cc := capacidades()
		require.Equal(t, 20, hc)
		require.Equal(t, 20, cc)
		a, err := db.Actividad(ctx, gap.ID)
		require.NoError(t, err)
		require.Equal(t, gap.CapacidadMaxima, a.CapacidadMaxima, "rollback: el tope no cambió")
	})

	t.Run("bajar_tope_ajusta_horario_y_clase_y_publica", func(t *testing.T) {
		var antes int
		require.NoError(t, db.pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE event_type='clase.actualizada' AND agregado_id=$1`, c.ID).Scan(&antes))
		_, err := db.ModificarActividad(ctx, uuidN(1), gap.ID, tope(5))
		require.NoError(t, err)
		hc, cc := capacidades()
		require.Equal(t, 5, hc)
		require.Equal(t, 5, cc)
		var despues int
		require.NoError(t, db.pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE event_type='clase.actualizada' AND agregado_id=$1`, c.ID).Scan(&despues))
		require.Equal(t, antes+1, despues, "la búsqueda y los cupos en vivo se enteran del cupo nuevo")
	})

	t.Run("subir_tope_no_toca_las_clases", func(t *testing.T) {
		_, err := db.ModificarActividad(ctx, uuidN(1), gap.ID, tope(30))
		require.NoError(t, err)
		hc, cc := capacidades()
		require.Equal(t, 5, hc)
		require.Equal(t, 5, cc)
	})
}
