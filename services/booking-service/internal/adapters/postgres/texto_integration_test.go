// Búsqueda por texto parcial contra PostgreSQL real (corrección del 13/11/2026,
// HU-13): cada palabra debe empezar alguna palabra de la actividad o del
// profesor, sin distinguir mayúsculas ni tildes.
package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/GuadiViti/Arquitectura-de-Software/services/booking-service/internal/domain"
)

func TestBusquedaTextoPostgres(t *testing.T) {
	db := nuevaBase(t)
	ctx := context.Background()
	inicio := time.Now().Add(72 * time.Hour).Truncate(time.Hour)
	zumba := clase(t, db, actividadPorNombre(t, db, "Zumba"), uuidN(700), inicio, 60, 10)
	muscu := clase(t, db, actividadPorNombre(t, db, "Musculación"), uuidN(701), inicio.Add(2*time.Hour), 60, 10)
	strong := clase(t, db, actividadPorNombre(t, db, "Strong Nation"), uuidN(702), inicio.Add(4*time.Hour), 60, 10)

	buscar := func(texto string) []string {
		t.Helper()
		palabras, err := domain.PalabrasBusqueda(texto)
		require.NoError(t, err)
		page, err := db.BuscarClases(ctx, domain.FiltroClases{Desde: inicio.Add(-time.Hour), Hasta: inicio.Add(24 * time.Hour), Palabras: palabras, Pagina: 1, Tamano: 20}, "")
		require.NoError(t, err)
		ids := []string{}
		for _, c := range page.Items {
			ids = append(ids, c.ID)
		}
		require.Equal(t, len(ids), page.Total)
		return ids
	}

	require.Equal(t, []string{zumba.ID}, buscar("zum"), "prefijo de la actividad")
	require.Equal(t, []string{zumba.ID}, buscar("ZUMBA"), "sin distinguir mayúsculas")
	require.Equal(t, []string{muscu.ID}, buscar("musculacion"), "sin distinguir tildes")
	require.Equal(t, []string{strong.ID}, buscar("nat"), "prefijo de la segunda palabra")
	require.Equal(t, []string{zumba.ID, muscu.ID, strong.ID}, buscar("luc"), "prefijo del profesor, en orden de inicio")
	require.Equal(t, []string{strong.ID}, buscar("str gom"), "todas las palabras deben coincidir")
	require.Empty(t, buscar("umba"), "solo comienzos de palabra, no el medio")
	require.Len(t, buscar(""), 3, "sin texto no filtra")
}
